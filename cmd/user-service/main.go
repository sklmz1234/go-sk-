// Command user-service 启动 user 服务的 gRPC server。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormotel "gorm.io/plugin/opentelemetry/tracing"

	"go-ecom-admin/pkg/config"
	"go-ecom-admin/pkg/database"
	"go-ecom-admin/pkg/logger"
	"go-ecom-admin/pkg/telemetry"

	"go-ecom-admin/internal/user/model"
	"go-ecom-admin/internal/user/repository"
	"go-ecom-admin/internal/user/service"
	userpb "go-ecom-admin/proto/user"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(logger.Config{
		Level:       cfg.Log.Level,
		Encoding:    cfg.Log.Encoding,
		OutputPaths: cfg.Log.OutputPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	// ctx 提前到数据库连接之前创建：启动期重试若撞上 SIGTERM
	// （K8s 滚动更新 / docker stop），重试循环要能立刻让位退出，
	// 而不是拖满宽限期才被强杀。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 链路追踪（阶段 2D）：作为被调方，负责从 gRPC metadata 里提取上游
	// 传播来的 trace 上下文并延续 span——网关和本服务因此能连成一条链。
	var tracerProvider *sdktrace.TracerProvider
	var meterProvider *sdkmetric.MeterProvider
	var metricsHandler http.Handler
	if cfg.Telemetry.Enabled {
		tracerProvider, err = telemetry.Setup(ctx, telemetry.Config{
			ServiceName:  "user-service",
			OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
			SampleRatio:  cfg.Telemetry.SampleRatio,
		})
		if err != nil {
			log.Fatal("init telemetry", zap.Error(err))
		}
		// 指标支柱（阶段 2D 下半程）：全局 MeterProvider 一设，
		// otelgrpc 服务端 handler 的每方法调用数/耗时指标自动出现——
		// 上半程选 stats.Handler 埋点的分红，这里一行埋点都不用加。
		meterProvider, metricsHandler, err = telemetry.SetupMetrics()
		if err != nil {
			log.Fatal("init metrics", zap.Error(err))
		}
	}

	// 注册、登录和密码校验都依赖持久化用户数据，因此服务启动时必须先确认
	// 数据库连接和表结构准备完成。初始化失败时立即结束进程，避免服务在无法
	// 正常处理请求的状态下继续监听，直到请求到来后才暴露数据库不可用的问题。
	//
	// 连接失败不直接 Fatal：Docker daemon 重启后各容器被并行拉起，
	// MySQL 可能还没就绪（depends_on 只在 compose up 编排时生效）。
	// 这里带退避重试 2 分钟（1s 起指数增长封顶 10s），期间秒级自愈；
	// 超时仍失败才 Fatal，交给 restart 策略 / K8s 兜底重启。
	// TranslateError: true 让 GORM 把驱动的方言错误（例如 MySQL 的 1062
	// 唯一键冲突）翻译成统一的 gorm.ErrDuplicatedKey——不开这个开关，
	// repository.Create 里的 errors.Is(err, gorm.ErrDuplicatedKey) 永远
	// 不会命中，重复用户名会被错误地当成 Internal(500) 而不是 409。
	db, err := database.ConnectWithRetry(ctx, func() (*gorm.DB, error) {
		return gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{TranslateError: true})
	}, database.ConnectConfig{Log: log})
	if err != nil {
		log.Fatal("failed to connect to MySQL", zap.Error(err))
	}
	// SQL span 埋点（阶段 2D）：GORM 插件在每次查询前后建 span。前提是
	// 查询带 ctx（repository 里已全部 WithContext）——span 从 gRPC handler
	// 的 ctx 一路传到 SQL，这就是"trace 贯穿到存储层"的最后一环。
	// WithoutQueryVariables：不把查询参数（含密码哈希）记进 span 属性，
	// 观测数据会进 Jaeger 这类几乎无访问控制的系统，敏感值不能落进去。
	// 指标（2D 下半程）不再禁用：插件读全局 MeterProvider，
	// SQL 耗时/错误指标随 SetupMetrics 自动开始上报。
	if err := db.Use(gormotel.NewPlugin(gormotel.WithoutQueryVariables())); err != nil {
		log.Fatal("attach gorm otel plugin", zap.Error(err))
	}
	// 带锁迁移：K8s 下 Deployment 是 2 副本，两个 Pod 同时启动时
	// 裸 AutoMigrate 会抢建表（Error 1050 → CrashLoop）。GET_LOCK 命名锁
	// 串行化后，先到者建表、后到者等锁再跑一遍幂等的 AutoMigrate。
	// Address（阶段 5B）是 mock 收货信息池（无 user_id 归属），见
	// internal/user/model/address.go 的注释。
	if err := database.Migrate(db, 30*time.Second, &model.User{}, &model.Address{}); err != nil {
		log.Fatal("auto migrate failed", zap.Error(err))
	}
	repo := repository.NewGormRepository(db)

	svc := service.New(repo, log, cfg.JWT.Secret, cfg.JWT.ExpireHours)

	// StatsHandler 承担服务端埋点：提取 metadata 里的 traceparent 延续
	// 上游链路 + 为每轮 RPC 建服务端 span。telemetry 未启用时全局 provider
	// 是 noop，无开销。
	grpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	userpb.RegisterUserServiceServer(grpcServer, svc)
	reflection.Register(grpcServer) // 方便本地用 grpcurl 调试，生产环境按需关闭

	// 注册 gRPC 标准健康检查服务（grpc.health.v1）：
	// K8s 原生 grpc 探针（readinessProbe/livenessProbe 的 grpc: 类型）就是
	// 调它的 Check 方法。不注册的话探针只能退化成 tcpSocket——端口通 ≠
	// 服务真的能处理请求，数据库挂了 tcpSocket 照样绿。健康包是 grpc
	// 模块自带的，不算新依赖。
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	addr := fmt.Sprintf(":%d", cfg.Server.UserService.GRPCPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal("failed to listen", zap.String("addr", addr), zap.Error(err))
	}

	// /metrics 运维端点：独立于 gRPC 业务端口的小 HTTP 服务（业务/运维
	// 端口分离，K8s 下可只对内网放开它）。gRPC 服务没有 gin 引擎可挂，
	// 所以不像网关那样复用业务端口。
	var metricsSrv *http.Server
	if metricsHandler != nil {
		metricsSrv = telemetry.NewMetricsServer(metricsHandler)
		go func() {
			log.Info("metrics endpoint listening", zap.Int("port", telemetry.MetricsPort))
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("metrics server stopped with error", zap.Error(err))
			}
		}()
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down user-service")
		grpcServer.GracefulStop()
		if metricsSrv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
				log.Error("metrics server shutdown", zap.Error(err))
			}
		}
		// 停服后 flush 未导出的 span，理由见 api-gateway main.go。
		if tracerProvider != nil {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerProvider.Shutdown(flushCtx); err != nil {
				log.Error("trace provider shutdown", zap.Error(err))
			}
		}
		if meterProvider != nil {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := meterProvider.Shutdown(flushCtx); err != nil {
				log.Error("meter provider shutdown", zap.Error(err))
			}
		}
	}()

	log.Info("user-service listening", zap.String("addr", addr))
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal("grpc server stopped with error", zap.Error(err))
	}
}
