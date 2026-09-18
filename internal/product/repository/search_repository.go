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

//Elasticsearch 商品搜索

const (
	// esDialTimeout 是 TCP 拨号超时：黑洞 IP 的快速失败线。
	esDialTimeout = time.Second
	// esHeaderTimeout 是等响应头的上限：连接已建立但对端不回包的场景。
	esHeaderTimeout = 2 * time.Second
	// esCallTimeout 是单次 ES 调用的总预算（含拨号+传输+服务端处理）。
	esCallTimeout = 2 * time.Second
)

// —— 启动期 ping 重试（2026-09-16 分类搜索事故驱动）——

var (
	esPingMaxWait = 30 * time.Second

	esPingMinDelay = time.Second

	esPingMaxDelay = 5 * time.Second

	esPingSleep = func(d time.Duration) { time.Sleep(d) }
)

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

type productDoc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
}

// indexMapping 建索引时的 mapping

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

// searchRepository：ES 召回 + MySQL 回表的装饰器
type searchRepository struct {
	next     Repository
	searcher ProductSearcher
	log      *zap.Logger
}

func NewSearchRepository(next Repository, searcher ProductSearcher, log *zap.Logger) Repository {
	return &searchRepository{next: next, searcher: searcher, log: log}
}

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
