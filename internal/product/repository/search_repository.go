package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"go.uber.org/zap"

	"go-ecom-admin/internal/product/model"
)

// —— 阶段 5A：Elasticsearch 商品搜索 ——
//
// 架构（搜索引擎的经典用法，面试可展开）：
//   ES 只做「召回」——索引里只放低变字段（name/description/image_url），
//   查询也只取回商品 id 列表（_source: false），按 _score 相关度排序；
//   MySQL 做「回表」——用 id 批量查出实时完整数据（stock/price 是高变字段，
//   下单扣减走 DeductStock 不会同步 ES，进索引必然过期）。
//
// 和缓存层同一个哲学：搜索引擎是加速层不是正确性依赖。
// ES 挂了 → searchRepository 装饰器降级到 gorm 的 LIKE；
// 服务启动时 ES 就连不上 → main 里干脆不包这层装饰器，裸 gorm 直接 LIKE。
//
// 装饰器同时承担「同步双写」：Create/Update/Delete 在 MySQL 成功后同步
// 维护 ES 文档（失败仅告警），让 C 端能立刻搜到管理台的变更。

// —— ES 调用的双层超时（验收 8 事故修复，2026-09-12 实测复盘）——
//
// 事故原型：docker stop elasticsearch 后容器 IP 从 Docker 网络消失，
// 没有主机回 RST——TCP SYN 发向黑洞，没有任何回应。ES 调用只有请求 ctx
// 兜底，于是一次搜索把整个 gRPC deadline（网关侧 5s）全部挂在拨号上；
// 降级 LIKE 虽然忠实执行，但拿到的是已取消的 ctx，出发即死，网关 504。
// 教训：降级机制要工作，前提是"失败要快"——慢失败会把兜底路径的
// 时间预算一起吃光。
//
// 两层防线对应两类故障：
//   - Transport 层（拨号 1s / 响应头 2s）：兜"连接层黑洞"——对端 IP 死了、
//     网络分区、SYN 无回应。这是本次事故的病灶层，必须在 TCP 层快速失败；
//   - ctx 层（每次调用 2s）：兜"业务级慢"——连接正常但 ES 内部慢查询/
//     GC 停顿。WithTimeout 与上游 deadline 取较早者，不会放大预算。
//
// 2s 的取值依据：搜索是浏览路径，用户对搜索的耐心以秒计；网关 gRPC
// deadline 是 5s，ES 花 2s 失败后，LIKE 兜底还有约 3s 预算——降级链路
// 被这个配比救活。
const (
	// esDialTimeout 是 TCP 拨号超时：黑洞 IP 的快速失败线。
	esDialTimeout = time.Second
	// esHeaderTimeout 是等响应头的上限：连接已建立但对端不回包的场景。
	esHeaderTimeout = 2 * time.Second
	// esCallTimeout 是单次 ES 调用的总预算（含拨号+传输+服务端处理）。
	esCallTimeout = 2 * time.Second
)

// —— 启动期 ping 重试（2026-09-16 分类搜索事故驱动）——
//
// 事故原型：compose up 时 ES 加载 IK 插件要十几秒，Go 服务一秒就绪；
// 启动 ping 只试一次，撞上 "connection refused" 就永久放弃——main 据此
// 不装配搜索装饰器，服务整个生命周期都在 LIKE 降级，ES 随后起来了也
// 不自知（装饰器是启动期装配的，运行期不会重新包）。
//
// 修法和 database.ConnectWithRetry（pkg/database/connect.go）同模式：
// 基础设施未就绪 → 应用层带退避重试，秒级间隔远快于容器重启的分钟级
// 退避。总预算 30s 覆盖带插件的 ES 冷启动；黑洞地址由 esDialTimeout
// 保证每次尝试 1s 内失败，不会把预算拖成灾难。
//
// 三个参数是包级 var 而非 const：单测要把预算缩到毫秒级、把 sleep
// 换成 no-op，避免真实等待拖慢 CI（connect.go 的 sleep 同手法）。
var (
	// esPingMaxWait 启动探活的总预算，耗尽才返回错误（main 才降级 LIKE）。
	esPingMaxWait = 30 * time.Second
	// esPingMinDelay 第一次重试前的等待（退避基数），随尝试指数翻倍。
	esPingMinDelay = time.Second
	// esPingMaxDelay 单次等待的上限（封顶 5s，避免后期退避太疏）。
	esPingMaxDelay = 5 * time.Second
	// esPingSleep 可替换的 sleep，测试里换成 no-op。
	esPingSleep = func(d time.Duration) { time.Sleep(d) }
)

// newESTransport 带超时边界的 HTTP 传输层。go-elasticsearch 的默认
// transport 沿用 http.DefaultTransport 的零超时拨号——对"对端已死但
// 网络不报错"的黑洞场景毫无防御，这是必须自定义的原因。
func newESTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   esDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: esHeaderTimeout,
		// 连接池参数与 http.DefaultTransport 对齐，只动超时相关字段。
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// withCallTimeout 给单次 ES 调用套上总预算。调用方（装饰器）传来的 ctx
// 可能带着网关的 5s deadline，也可能已经死了（那调用会立刻失败，
// 交给降级路径）——WithTimeout 取较早者，语义天然正确。
func (s *ESSearcher) withCallTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, esCallTimeout)
}

// ProductSearcher 抽象搜索引擎的最小能力集。装饰器面向这个接口编程，
// 单测可以用假实现注入（不用起真 ES），和 Repository 接口的用意一致。
type ProductSearcher interface {
	// Search 按关键词召回商品 id，按相关度（_score）降序，附带命中总数。
	Search(ctx context.Context, keyword string, page, pageSize int) (ids []uint64, total int64, err error)
	// Index 写入/覆盖一个商品文档（doc _id = 商品 id，天然幂等）。
	Index(ctx context.Context, p *model.Product) error
	// Delete 删除商品文档。商品已被删除时再删是 no-op，不算错误。
	Delete(ctx context.Context, id uint64) error
}

// ESSearcher 是 ProductSearcher 的 Elasticsearch 实现。
type ESSearcher struct {
	client *elasticsearch.Client
	index  string
	log    *zap.Logger
}

// productDoc 是 ES 文档结构。刻意没有 stock/price：高变字段进索引必然
// 过期（下单扣库存不会双写 ES），展示用的实时数据一律回表拿。
type productDoc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
}

// indexMapping 建索引时的 mapping：
//   - name/description 用 IK 分词（ik_max_word 索引时最细粒度切词，
//     ik_smart 查询时粗粒度）——standard 分词器把中文按单字切，相关度
//     排序会失效，这是选 IK 的直接原因（镜像 Dockerfile 里有完整注释）；
//   - image_url 是 keyword 且 index:false——只存储不参与搜索，省索引体积；
//     其实 _source:false 的查询用不到它，写入它是为了以后想"只读 ES 不回表"
//     的优化留余地（届时改动只在查询侧）。
const indexMapping = `{
  "mappings": {
    "properties": {
      "name":        {"type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart"},
      "description": {"type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart"},
      "image_url":   {"type": "keyword", "index": false}
    }
  }
}`

// NewESSearcher 连接 ES 并确保索引存在（不存在则按 IK mapping 创建）。
// 启动探活带退避重试（见上面的 var 块注释）——重试预算耗尽才返回错误，
// 调用方（main）据此决定不启用搜索装饰器，整个服务降级为 LIKE。
func NewESSearcher(addr, index string, log *zap.Logger) (*ESSearcher, error) {
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{addr},
		Transport: newESTransport(),
	})
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: new client: %w", err)
	}
	s := &ESSearcher{client: client, index: index, log: log}

	if err := s.pingWithRetry(client); err != nil {
		return nil, err
	}

	if err := s.ensureIndex(context.Background()); err != nil {
		return nil, fmt.Errorf("elasticsearch: ensure index: %w", err)
	}
	return s, nil
}

// pingWithRetry 启动期探活：带退避反复 ping，成功或总预算耗尽才返回。
// 与 database.ConnectWithRetry 同一个循环骨架：成功 → nil；预算耗尽 →
// 包装最后一次错误的 error（main 拿到它降级 LIKE 并记 WARN）。
func (s *ESSearcher) pingWithRetry(client *elasticsearch.Client) error {
	deadline := time.Now().Add(esPingMaxWait)
	delay := esPingMinDelay

	for attempt := 1; ; attempt++ {
		// 单次 ping 同样受 esCallTimeout 约束：黑洞地址 1s 内失败，
		// 不会把重试预算变成"每次都挂满"。
		pingCtx, cancel := s.withCallTimeout(context.Background())
		res, err := client.Ping(client.Ping.WithContext(pingCtx))
		cancel()
		if err == nil {
			if !res.IsError() {
				res.Body.Close()
				if attempt > 1 {
					s.log.Info("elasticsearch reachable after retries", zap.Int("attempts", attempt))
				}
				return nil
			}
			err = fmt.Errorf("elasticsearch: ping: status %s", res.Status())
			res.Body.Close()
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("elasticsearch: unreachable after %d attempts over %s: %w",
				attempt, esPingMaxWait, err)
		}

		wait := min(delay, remaining)
		s.log.Warn("elasticsearch not ready, will retry",
			zap.Int("attempt", attempt),
			zap.Duration("retry_in", wait),
			zap.Error(err))

		esPingSleep(wait)
		delay = min(delay*2, esPingMaxDelay)
	}
}

// Recreate 删除并重建索引（带 IK mapping）。只给 seed / 未来的 reindex
// 脚本用，业务服务启动永远走 ensureIndex 不调这里。
// 为什么 seed 必须重建而不是"不存在才建"：seed 对 MySQL 是 TRUNCATE，
// 自增 id 归零重用，旧索引里的文档 id 和新库必然对不上——seed 的语义是
// "确定的初始状态"，ES 索引也是状态的一部分。
func (s *ESSearcher) Recreate(ctx context.Context) error {
	res, err := s.client.Indices.Delete([]string{s.index},
		s.client.Indices.Delete.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: delete index: %w", err)
	}
	defer res.Body.Close()
	// 404 = 索引本来就不存在（首次 seed）——删除的语义是"确保不存在"，
	// 已经不存在就是目标状态，不算错误（和 Delete 文档的 404 处理同理）。
	if res.IsError() && res.StatusCode != 404 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("elasticsearch: delete index %q: %s: %s", s.index, res.Status(), strings.TrimSpace(string(body)))
	}

	if err := s.ensureIndex(ctx); err != nil {
		return err
	}
	s.log.Info("elasticsearch index recreated", zap.String("index", s.index))
	return nil
}

// ensureIndex 索引不存在才创建。已存在时完全不动——mapping 变更（比如
// 以后加字段）需要显式重建，启动时静默改 mapping 是事故温床。
func (s *ESSearcher) ensureIndex(ctx context.Context) error {
	res, err := s.client.Indices.Exists([]string{s.index}, s.client.Indices.Exists.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 200 {
		return nil
	}

	createRes, err := s.client.Indices.Create(s.index,
		s.client.Indices.Create.WithContext(ctx),
		s.client.Indices.Create.WithBody(strings.NewReader(indexMapping)),
	)
	if err != nil {
		return err
	}
	defer createRes.Body.Close()
	if createRes.IsError() {
		body, _ := io.ReadAll(createRes.Body)
		return fmt.Errorf("create index %q: %s: %s", s.index, createRes.Status(), strings.TrimSpace(string(body)))
	}
	s.log.Info("elasticsearch index created", zap.String("index", s.index))
	return nil
}

// Search 用 multi_match 在 name（权重 ×2）和 description 上查相关度。
// name 加权是因为商品名命中比描述命中更能代表用户意图（搜"耳机"，
// 名字叫耳机的应该排在描述里提了一嘴耳机的前面）。
//
// _source:false 是"召回+回表"架构的关键：查询只取 _id 和 _score，
// 不回传文档内容——网络开销最小，也从代码上固化了"详情必须回表拿实时的"
// 这个纪律。
func (s *ESSearcher) Search(ctx context.Context, keyword string, page, pageSize int) ([]uint64, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}

	ctx, cancel := s.withCallTimeout(ctx)
	defer cancel()

	query := map[string]any{
		"_source": false,
		"query": map[string]any{
			"multi_match": map[string]any{
				"query":  keyword,
				"fields": []string{"name^2", "description"},
			},
		},
		"from": (page - 1) * pageSize,
		"size": pageSize,
		// 7.x 起 total 默认只统计到 10000 上限，分页 total 展示要精确值。
		"track_total_hits": true,
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, 0, fmt.Errorf("elasticsearch: marshal query: %w", err)
	}

	res, err := s.client.Search(
		s.client.Search.WithContext(ctx),
		s.client.Search.WithIndex(s.index),
		s.client.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("elasticsearch: search: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		respBody, _ := io.ReadAll(res.Body)
		return nil, 0, fmt.Errorf("elasticsearch: search: %s: %s", res.Status(), strings.TrimSpace(string(respBody)))
	}

	// 只解析用得上的字段，不做全量文档反序列化（_source 本来就是关的）。
	var parsed struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, 0, fmt.Errorf("elasticsearch: decode response: %w", err)
	}

	ids := make([]uint64, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		id, err := strconv.ParseUint(hit.ID, 10, 64)
		if err != nil {
			// doc _id 全部来自本服务的 Index（商品 id），解析失败说明
			// 索引里混入了异物——跳过并留日志，不让一条脏数据搞挂整个搜索。
			s.log.Warn("elasticsearch hit has non-numeric id, skipped",
				zap.String("hit_id", hit.ID), zap.String("index", s.index))
			continue
		}
		ids = append(ids, id)
	}
	return ids, parsed.Hits.Total.Value, nil
}

// Index 写入/覆盖商品文档。doc _id 用商品 id：同一商品重复写是覆盖
// 而不是新增，天然幂等——双写重试不会产生重复文档。
// Refresh:true 让写入立即可搜（默认 1s 刷新间隔内搜不到），
// 学习项目数据量小，用写延迟换"管理台改完 C 端立刻搜到"的体验。
func (s *ESSearcher) Index(ctx context.Context, p *model.Product) error {
	body, err := json.Marshal(productDoc{
		Name:        p.Name,
		Description: p.Description,
		ImageURL:    p.ImageURL,
	})
	if err != nil {
		return fmt.Errorf("elasticsearch: marshal doc: %w", err)
	}

	ctx, cancel := s.withCallTimeout(ctx)
	defer cancel()

	res, err := s.client.Index(s.index, bytes.NewReader(body),
		s.client.Index.WithContext(ctx),
		s.client.Index.WithDocumentID(strconv.FormatUint(p.ID, 10)),
		s.client.Index.WithRefresh("true"),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: index doc: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		respBody, _ := io.ReadAll(res.Body)
		return fmt.Errorf("elasticsearch: index doc %d: %s: %s", p.ID, res.Status(), strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (s *ESSearcher) Delete(ctx context.Context, id uint64) error {
	ctx, cancel := s.withCallTimeout(ctx)
	defer cancel()

	res, err := s.client.Delete(s.index, strconv.FormatUint(id, 10),
		s.client.Delete.WithContext(ctx),
		s.client.Delete.WithRefresh("true"),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: delete doc: %w", err)
	}
	defer res.Body.Close()
	// 404 = 文档本来就不存在（比如商品创建于 ES 上线之前）——
	// 删除的语义是"确保不存在"，已经不存在就是目标状态，不算错误。
	if res.IsError() && res.StatusCode != 404 {
		respBody, _ := io.ReadAll(res.Body)
		return fmt.Errorf("elasticsearch: delete doc %d: %s: %s", id, res.Status(), strings.TrimSpace(string(respBody)))
	}
	return nil
}

// —— searchRepository：ES 召回 + MySQL 回表的装饰器 ——

// searchRepository 和 cachedRepository 是同一个模式的第二次应用：
// 实现同一个 Repository 接口，包住下一层（gorm 实现），在
// SearchByKeyword 一个方法上做增强（ES 召回），其余方法全部透传。
type searchRepository struct {
	next     Repository
	searcher ProductSearcher
	log      *zap.Logger
}

// NewSearchRepository 用搜索引擎包装一个已有 Repository，返回的仍是
// Repository 接口。service 层感知不到搜索实现的存在。
func NewSearchRepository(next Repository, searcher ProductSearcher, log *zap.Logger) Repository {
	return &searchRepository{next: next, searcher: searcher, log: log}
}

// SearchByKeyword 三步走：ES 召回 id（按 _score 有序）→ MySQL 回表
// 拿实时完整数据 → 按召回顺序重排。
//
// 为什么必须重排：WHERE id IN (...) 不保证返回顺序（MySQL 按主键/
// 索引顺序返回），不重排的话相关度排序在回表这一步就丢了——
// "召回+回表"架构里最容易漏的细节。
//
// ES 故障降级：召回失败时退回下一层的 LIKE 实现并记 WARN。
// 搜索是浏览路径不是交易路径，"搜得粗糙"好过"搜不了"——
// 和 cachedRepository "Redis 故障降级回源"（cached_repository.go:77）
// 是同一个取舍。
func (r *searchRepository) SearchByKeyword(ctx context.Context, keyword string, page, pageSize int) ([]*model.Product, int64, error) {
	ids, total, err := r.searcher.Search(ctx, keyword, page, pageSize)
	if err != nil {
		r.log.Warn("elasticsearch search failed, falling back to MySQL LIKE",
			zap.String("keyword", keyword), zap.Error(err))
		return r.next.SearchByKeyword(ctx, keyword, page, pageSize)
	}
	if len(ids) == 0 {
		return []*model.Product{}, total, nil
	}

	products, err := r.next.ListByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}

	// 按 ES 召回顺序重排：建 id → product 索引，按 ids 顺序重取。
	byID := make(map[uint64]*model.Product, len(products))
	for _, p := range products {
		byID[p.ID] = p
	}
	ordered := make([]*model.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			ordered = append(ordered, p)
		}
		// id 在 ES 有但 MySQL 没有 = 双写不一致（商品删了但 ES 文档没删掉）。
		// 跳过即可，降级 LIKE 和后续 reindex 会收敛这种脏数据。
	}
	return ordered, total, nil
}

// 以下方法全部透传——装饰器只增强搜索（SearchByKeyword）和双写
// （Create/Update/Delete），其余方法和缓存装饰器共享同一条纪律：
// 增强点显式可见，不存在"悄悄改变行为"的方法。

// —— 同步双写（阶段 5A，决策点 B）——
//
// Create/Update/Delete 在 MySQL 成功后同步写 ES，失败只记 WARN 不阻塞
// 主流程：搜索引擎是加速层不是正确性依赖（和缓存同哲学），如果写 ES 挂了
// 就让请求失败，等于把 ES 的可用性绑进了交易路径。不一致窗口的兜底：
// 查询侧降级 LIKE + 重跑 seed 全量重建（Recreate）。
//
// 为什么双写收敛在装饰器而不是 service 层：ES 的存在对 service 完全透明
// （main 里 ES 不可用就不包这层装饰器）。如果把 Index 调用写进
// service.CreateProduct，service 就得多持有一个 ProductSearcher 依赖并
// 处理"ES 没启用怎么办"的分支——装饰器模式让"有没有搜索引擎"这个差异
// 只出现在 main 的装配代码里。
//
// DeductStock/RestoreStock 保持纯透传不写 ES：stock 本来就不在索引里
// （见 productDoc 注释），高变字段天然免同步——这是"索引只放低变字段"
// 设计直接兑现的红利。

func (r *searchRepository) Create(ctx context.Context, p *model.Product) error {
	if err := r.next.Create(ctx, p); err != nil {
		return err
	}
	// p.ID 已由 gorm 回填；doc _id = 商品 id，重复写是覆盖（天然幂等）。
	if err := r.searcher.Index(ctx, p); err != nil {
		r.log.Warn("elasticsearch index on create failed, product invisible to search until reseed",
			zap.Uint64("product_id", p.ID), zap.Error(err))
	}
	return nil
}

func (r *searchRepository) GetByID(ctx context.Context, id uint64) (*model.Product, error) {
	return r.next.GetByID(ctx, id)
}

func (r *searchRepository) Update(ctx context.Context, p *model.Product) error {
	if err := r.next.Update(ctx, p); err != nil {
		return err
	}
	// service 的 Update 是整体替换语义，p 上带着最新的 name/description/
	// image_url，直接索引 p 即可，不用为 ES 多查一次库。
	if err := r.searcher.Index(ctx, p); err != nil {
		r.log.Warn("elasticsearch index on update failed, search may serve stale doc until reseed",
			zap.Uint64("product_id", p.ID), zap.Error(err))
	}
	return nil
}

func (r *searchRepository) Delete(ctx context.Context, id uint64) error {
	if err := r.next.Delete(ctx, id); err != nil {
		return err
	}
	if err := r.searcher.Delete(ctx, id); err != nil {
		r.log.Warn("elasticsearch delete failed, stale doc may be recalled until reseed",
			zap.Uint64("product_id", id), zap.Error(err))
	}
	return nil
}

func (r *searchRepository) List(ctx context.Context, page, pageSize int) ([]*model.Product, int64, error) {
	return r.next.List(ctx, page, pageSize)
}

func (r *searchRepository) ListByIDs(ctx context.Context, ids []uint64) ([]*model.Product, error) {
	return r.next.ListByIDs(ctx, ids)
}

func (r *searchRepository) DeductStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error) {
	return r.next.DeductStock(ctx, productID, quantity)
}

func (r *searchRepository) RestoreStock(ctx context.Context, productID uint64, quantity int32) (*model.Product, error) {
	return r.next.RestoreStock(ctx, productID, quantity)
}

func (r *searchRepository) RestoreStockIdempotent(ctx context.Context, messageID string, productID uint64, quantity int32) (*model.Product, error) {
	return r.next.RestoreStockIdempotent(ctx, messageID, productID, quantity)
}

// 编译期断言：装饰器必须始终实现完整 Repository 接口，
// 接口加方法时漏了透传会在编译期暴露，而不是线上才发现。
var _ Repository = (*searchRepository)(nil)
