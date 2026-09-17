// Command product-service 启动 product 服务的 gRPC server。
// 结构与 cmd/user-service/main.go 完全对称，理由见那边的注释。
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

	"go-ecom-admin/pkg/cache"
	"go-ecom-admin/pkg/config"
	"go-ecom-admin/pkg/database"
	"go-ecom-admin/pkg/logger"
	"go-ecom-admin/pkg/telemetry"

	"go-ecom-admin/internal/product/model"
	"go-ecom-admin/internal/product/repository"
	"go-ecom-admin/internal/product/service"
	productpb "go-ecom-admin/proto/product"
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

	// ctx 提前到数据库连接之前创建：启动期重试若撞上 SIGTERM，
	// 重试循环要能立刻让位退出（理由见 cmd/user-service/main.go）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 链路追踪（阶段 2D）：理由见 cmd/user-service/main.go。
	var tracerProvider *sdktrace.TracerProvider
	var meterProvider *sdkmetric.MeterProvider
	var metricsHandler http.Handler
	if cfg.Telemetry.Enabled {
		tracerProvider, err = telemetry.Setup(ctx, telemetry.Config{
			ServiceName:  "product-service",
			OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
			SampleRatio:  cfg.Telemetry.SampleRatio,
		})
		if err != nil {
			log.Fatal("init telemetry", zap.Error(err))
		}
		// 指标支柱（2D 下半程），理由见 cmd/user-service/main.go：
		// MeterProvider 设置后 otelgrpc 指标自动出现。
		meterProvider, metricsHandler, err = telemetry.SetupMetrics()
		if err != nil {
			log.Fatal("init metrics", zap.Error(err))
		}
	}

	// 和 user-service 一样：现在依赖真实数据（库存/价格都要能改），
	// 连不上数据库最终会 Fatal。但"基础设施暂时未就绪"（daemon 重启后
	// MySQL 还在初始化）和"配置错误"要区分开：前者带退避重试 2 分钟
	// 秒级自愈，超时才 Fatal 交给 restart 策略兜底，机制详见
	// pkg/database/connect.go 的注释。
	// TranslateError: 驱动方言错误(如 MySQL 1062)→gorm.ErrDuplicatedKey 等统一错误
	db, err := database.ConnectWithRetry(ctx, func() (*gorm.DB, error) {
		return gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{TranslateError: true})
	}, database.ConnectConfig{Log: log})
	if err != nil {
		log.Fatal("failed to connect to MySQL", zap.Error(err))
	}
	// SQL span 埋点，理由见 cmd/user-service/main.go。
	// 指标（2D 下半程）不再禁用，随全局 MeterProvider 自动上报。
	if err := db.Use(gormotel.NewPlugin(gormotel.WithoutQueryVariables())); err != nil {
		log.Fatal("attach gorm otel plugin", zap.Error(err))
	}
	// 带锁迁移，理由见 cmd/user-service/main.go：多副本并发建表竞态。
	// CartItem（阶段 5B）与 Product 同库：ListCart 的 JOIN 在单库内完成。
	if err := database.Migrate(db, 30*time.Second, &model.Product{}, &model.StockRestore{}, &model.CartItem{}); err != nil {
		log.Fatal("auto migrate failed", zap.Error(err))
	}
	repo := repository.NewGormRepository(db)

	// 搜索装饰器（阶段 5A）：ES 可用就把 gorm 实现包进"ES 召回 + 回表"
	// 装饰器，SearchByKeyword 走搜索引擎；ES 连不上时不包这层，
	// SearchByKeyword 落到 gorm 的 LIKE 兜底实现——搜索引擎是加速层
	// 不是正确性依赖，和下面缓存装饰器的降级原则完全一致。
	esSearcher, err := repository.NewESSearcher(cfg.Elasticsearch.Addr, cfg.Elasticsearch.Index, log)
	if err != nil {
		log.Warn("elasticsearch unavailable, product search falls back to MySQL LIKE", zap.Error(err))
	} else {
		repo = repository.NewSearchRepository(repo, esSearcher, log)
		log.Info("elasticsearch search enabled", zap.String("addr", cfg.Elasticsearch.Addr), zap.String("index", cfg.Elasticsearch.Index))
	}

	// 缓存装饰器（阶段 2C）：Redis 可用就把 gorm 实现包进 cache-aside 装饰器，
	// service 层拿到的仍是同一个 Repository 接口，零改动。
	// Redis 连不上时降级为直连 MySQL——缓存是加速层不是正确性依赖，
	// 缓存挂了服务应该变慢而不是变挂（和装饰器里"Redis 故障降级回源"同一原则）。
	rdb, err := cache.New(cache.Config{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	if err != nil {
		log.Warn("redis unavailable, product cache disabled (falling back to direct MySQL)", zap.Error(err))
	} else {
		defer rdb.Close()
		repo = repository.NewCachedRepository(repo, rdb)
		log.Info("product cache enabled", zap.String("redis", cfg.Redis.Addr))
	}

	svc := service.New(repo, log)

	// CartService（阶段 5B）与 ProductService 同进程注册：共享同一个商品
	// Repository（存在性检查命中缓存），但购物车有独立的 Repository（无
	// 缓存/搜索装饰器，见 cart_repository.go 的包注释）。一个进程暴露两个
	// gRPC service 是完全常规的形态——服务边界按业务划分，不按进程数。
	cartSvc := service.NewCartService(repo, repository.NewGormCartRepository(db), log)

	// 服务端埋点：提取上游 trace 上下文 + 每轮 RPC 建服务端 span。
	grpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	productpb.RegisterProductServiceServer(grpcServer, svc)
	productpb.RegisterCartServiceServer(grpcServer, cartSvc)
	reflection.Register(grpcServer)

	// 同 user-service：注册标准健康检查服务，供 K8s 原生 grpc 探针使用，
	// 详见 cmd/user-service/main.go 里的注释。
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	addr := fmt.Sprintf(":%d", cfg.Server.ProductService.GRPCPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal("failed to listen", zap.String("addr", addr), zap.Error(err))
	}

	// /metrics 运维端点，理由见 cmd/user-service/main.go：业务/运维端口分离。
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
		log.Info("shutting down product-service")
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

	log.Info("product-service listening", zap.String("addr", addr))
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal("grpc server stopped with error", zap.Error(err))
	}
}
