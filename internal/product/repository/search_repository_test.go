// 搜索链路（阶段 5A）单元测试，分两层：
//
//   - searchRepository 装饰器：用假 ProductSearcher + MockRepository 验证
//     "ES 召回 → 回表 → 按 _score 重排" 和 "ES 故障降级 LIKE" 两条路径，
//     不需要起真 ES（装饰器面向 ProductSearcher 接口编程的回报）；
//   - gormRepository 的 LIKE 兜底实现：sqlite :memory: 跑真实 SQL，
//     重点考点是 LIKE 通配符转义（用户输入的 % 不能变成全表匹配）。
package repository

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go-ecom-admin/internal/product/model"
	"go-ecom-admin/internal/product/repository/mocks"
)

// fakeSearcher 是 ProductSearcher 的测试替身：Search 返回预置结果，
// Index/Delete 录制调用（双写测试的断言对象），也可注入错误模拟 ES 故障。
type fakeSearcher struct {
	ids   []uint64
	total int64
	err   error

	indexed   []*model.Product
	deleted   []uint64
	indexErr  error
	deleteErr error
}

func (f *fakeSearcher) Search(_ context.Context, _ string, _, _ int) ([]uint64, int64, error) {
	return f.ids, f.total, f.err
}

func (f *fakeSearcher) Index(_ context.Context, p *model.Product) error {
	f.indexed = append(f.indexed, p)
	return f.indexErr
}

func (f *fakeSearcher) Delete(_ context.Context, id uint64) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

// —— 同步双写（决策点 B）测试 ——

// TestCreate_DualWritesES：MySQL 写入成功后必须同步索引同一商品到 ES，
// 这是"管理台建商品、C 端立刻能搜到"（验收第 7 条）的机制保证。
func TestCreate_DualWritesES(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{}
	p := &model.Product{Name: "蓝牙耳机", Description: "主动降噪", ImageURL: "https://x/y.jpg"}
	next.EXPECT().Create(mock.Anything, p).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Create(context.Background(), p))

	require.Len(t, searcher.indexed, 1)
	assert.Same(t, p, searcher.indexed[0])
}

// TestCreate_ESFailureDoesNotBlock：双写失败只能记 WARN，主流程照常成功——
// ES 是加速层不是正确性依赖，它的可用性不能绑进交易路径。
func TestCreate_ESFailureDoesNotBlock(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{indexErr: errors.New("es: connection refused")}
	p := &model.Product{Name: "蓝牙耳机"}
	next.EXPECT().Create(mock.Anything, p).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Create(context.Background(), p), "ES 双写失败不能让 Create 失败")
	assert.Len(t, searcher.indexed, 1, "索引动作确实尝试过")
}

// TestCreate_MySQLFailureSkipsES：MySQL 没写成就不能写 ES，
// 否则索引里会出现库里不存在的幽灵商品（搜得到、点详情 404）。
func TestCreate_MySQLFailureSkipsES(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{}
	p := &model.Product{Name: "蓝牙耳机"}
	next.EXPECT().Create(mock.Anything, p).Return(errors.New("db: deadlock"))

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.Error(t, repo.Create(context.Background(), p))
	assert.Empty(t, searcher.indexed)
}

// TestUpdate_DualWritesES：service 的 Update 是整体替换语义，传给 repo 的
// model 带着最新 name/description/image_url，装饰器应原样索引它。
func TestUpdate_DualWritesES(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{}
	p := &model.Product{ID: 7, Name: "新名字耳机", Description: "改名后"}
	next.EXPECT().Update(mock.Anything, p).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Update(context.Background(), p))

	require.Len(t, searcher.indexed, 1)
	assert.Equal(t, "新名字耳机", searcher.indexed[0].Name)
}

// TestUpdate_ESFailureDoesNotBlock：同 Create——改名后 ES 写失败，
// 顶多是搜索里暂时还是旧名字（降级 LIKE 兜底），不能算更新失败。
func TestUpdate_ESFailureDoesNotBlock(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{indexErr: errors.New("es: circuit open")}
	p := &model.Product{ID: 7, Name: "新名字耳机"}
	next.EXPECT().Update(mock.Anything, p).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Update(context.Background(), p))
}

// TestDelete_DualWritesES：删除商品必须同步删 ES 文档，
// 否则会搜到已删除的商品（回表时被跳过，但 total 虚高）。
func TestDelete_DualWritesES(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{}
	next.EXPECT().Delete(mock.Anything, uint64(7)).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Delete(context.Background(), 7))

	assert.Equal(t, []uint64{7}, searcher.deleted)
}

// TestDelete_ESFailureDoesNotBlock：ES 删不掉只记 WARN，残留文档
// 在回表重排时被跳过，重跑 seed / reindex 会收敛。
func TestDelete_ESFailureDoesNotBlock(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{deleteErr: errors.New("es: timeout")}
	next.EXPECT().Delete(mock.Anything, uint64(7)).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Delete(context.Background(), 7))
	assert.Equal(t, []uint64{7}, searcher.deleted)
}

// TestESSearcher_SlowESFailsFast 是验收 8 事故（2026-09-12 实测）的回归测试：
// ES"半死不活"（连接能建但迟迟不响应）时，Search 必须在秒级快速失败——
// 没有双层超时之前，这个调用会一直挂到上游 gRPC deadline，把降级 LIKE
// 的时间预算也吃光，网关 504。假 ES 永不主动响应，断言客户端 ~2s 放弃。
func TestESSearcher_SlowESFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟"半死不活"的 ES：永不主动响应。客户端没有超时的话，
		// 这个测试会挂到下面的 10s 兜底（事故前行为是挂到上游 deadline）。
		// 注意 ResponseHeaderTimeout 触发后客户端不会关 TCP 连接
		// （连接保持 active），所以服务端不能只等 ctx——否则
		// httptest.Server.Close 会永远阻塞在等这条连接上。
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{srv.URL},
		Transport: newESTransport(),
	})
	require.NoError(t, err)
	s := &ESSearcher{client: client, index: "products", log: zaptest.NewLogger(t)}

	start := time.Now()
	_, _, err = s.Search(context.Background(), "耳机", 1, 20)
	elapsed := time.Since(start)

	require.Error(t, err, "慢 ES 必须报错，让装饰器走降级")
	// ResponseHeaderTimeout 2s + 少量调度余量；若退化回"挂到死"，这里会接近 10s。
	assert.Less(t, elapsed, 5*time.Second, "ES 无响应时必须秒级失败，不能挂到上游 deadline")
}

// TestSearchByKeyword_ReordersByRecallOrder 是"召回+回表"架构的核心用例：
// WHERE id IN (...) 不保证顺序（MockRepository 故意按主键序返回），
// 装饰器必须把结果重排回 ES 的 _score 相关度顺序——不重排的话，
// 用户搜到的"最相关商品"会消失在列表中间。
func TestSearchByKeyword_ReordersByRecallOrder(t *testing.T) {
	next := mocks.NewMockRepository(t)
	// ES 的相关度排序：3 最相关，1 次之，2 最后。
	searcher := &fakeSearcher{ids: []uint64{3, 1, 2}, total: 3}
	// 回表故意按主键序返回（模拟 MySQL 的实际行为）。
	next.EXPECT().ListByIDs(mock.Anything, []uint64{3, 1, 2}).Return([]*model.Product{
		{ID: 1, Name: "降噪耳机"},
		{ID: 2, Name: "耳机收纳盒"},
		{ID: 3, Name: "蓝牙耳机"},
	}, nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	products, total, err := repo.SearchByKeyword(context.Background(), "耳机", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, products, 3)
	assert.Equal(t, uint64(3), products[0].ID, "必须保持 ES 的相关度顺序")
	assert.Equal(t, uint64(1), products[1].ID)
	assert.Equal(t, uint64(2), products[2].ID)
}

// TestSearchByKeyword_FallbackOnESFailure 验证降级：ES 召回失败时
// 退回下一层的 LIKE 实现，搜索是浏览路径，"搜得粗糙"好过"搜不了"。
func TestSearchByKeyword_FallbackOnESFailure(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{err: errors.New("connection refused")}
	// 降级后走的是下一层的 SearchByKeyword（gorm LIKE 实现）。
	next.EXPECT().SearchByKeyword(mock.Anything, "耳机", 1, 20).Return([]*model.Product{
		{ID: 1, Name: "蓝牙耳机"},
	}, int64(1), nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	products, total, err := repo.SearchByKeyword(context.Background(), "耳机", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, products, 1)
	assert.Equal(t, "蓝牙耳机", products[0].Name)
}

// TestSearchByKeyword_EmptyRecall 召回为空时短路返回，不打回表这一枪
// （mock 未设 ListByIDs 期望，调了就算失败）。
func TestSearchByKeyword_EmptyRecall(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{ids: []uint64{}, total: 0}

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	products, total, err := repo.SearchByKeyword(context.Background(), "不存在的商品", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, products)
}

// TestSearchByKeyword_LIKE 验证 gorm 的 LIKE 兜底实现（sqlite 真实 SQL）：
// 名称包含关键词的商品被命中，不相关的被过滤，total 与分页正确。
func TestSearchByKeyword_LIKE(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	seedProduct(t, repo, "蓝牙耳机", 19900, 10)
	seedProduct(t, repo, "降噪耳机", 29900, 5)
	seedProduct(t, repo, "机械键盘", 39900, 3)

	products, total, err := repo.SearchByKeyword(context.Background(), "耳机", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, products, 2)
}

// TestSearchByKeyword_LIKEEscapesWildcards 是转义的核心用例：
// 用户输入 "100%" 里的 % 必须被当成字面量，而不是 LIKE 的任意匹配符——
// 不转义的话这个搜索会命中全表。
func TestSearchByKeyword_LIKEEscapesWildcards(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	seedProduct(t, repo, "蓝牙耳机", 19900, 10)
	seedProduct(t, repo, "机械键盘", 39900, 3)

	products, total, err := repo.SearchByKeyword(context.Background(), "100%", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(0), total, "% 必须按字面量匹配，不能命中任何商品")
	assert.Empty(t, products)
}

// TestSearchByKeyword_LIKEEscapeCharItself 转义符（!）自身必须按字面量匹配：
// 名字里真带 ! 的商品能被 "!" 搜到，且不能触发 SQL 错误。
// （转义符从 '\' 换成 '!' 的方言教训见 gorm 实现的注释。）
func TestSearchByKeyword_LIKEEscapeCharItself(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	seedProduct(t, repo, "清仓!蓝牙耳机", 9900, 1)
	seedProduct(t, repo, "机械键盘", 39900, 3)

	products, total, err := repo.SearchByKeyword(context.Background(), "清仓!", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, products, 1)
	assert.Equal(t, "清仓!蓝牙耳机", products[0].Name)
}

// TestListByIDs 验证回表批量查询：只取目标 id，空输入短路不打 SQL。
func TestListByIDs(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	p1 := seedProduct(t, repo, "蓝牙耳机", 19900, 10)
	seedProduct(t, repo, "机械键盘", 39900, 3)
	p3 := seedProduct(t, repo, "降噪耳机", 29900, 5)

	products, err := repo.ListByIDs(context.Background(), []uint64{p1.ID, p3.ID})

	require.NoError(t, err)
	require.Len(t, products, 2)
	ids := []uint64{products[0].ID, products[1].ID}
	assert.Contains(t, ids, p1.ID)
	assert.Contains(t, ids, p3.ID)

	empty, err := repo.ListByIDs(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// —— 2026-09-16 分类搜索事故的回归测试对 ——

// setFastPingRetry 把 ping 重试的等待全部"加速"：预算缩到毫秒级、
// sleep 换成 no-op，让重试循环在单测里瞬间跑完。t.Cleanup 在测试结束
// 时恢复全局（defer 在 helper 里会在返回时就恢复，等于没改）。
func setFastPingRetry(t *testing.T, maxWait time.Duration) {
	t.Helper()
	origWait, origMin, origMax, origSleep := esPingMaxWait, esPingMinDelay, esPingMaxDelay, esPingSleep
	t.Cleanup(func() {
		esPingMaxWait, esPingMinDelay, esPingMaxDelay, esPingSleep = origWait, origMin, origMax, origSleep
	})
	esPingMaxWait = maxWait
	esPingSleep = func(time.Duration) {}
}

// TestPingWithRetry_SucceedsAfterTransientFailures 是启动竞态的核心用例：
// ES 先 500 两下、随后恢复，NewESSearcher 必须重试等到恢复，而不是第一次
// 失败就放弃（放弃 = main 不装装饰器，服务整个生命周期都在 LIKE 降级，
// 这正是 2026-09-16 事故：ES 慢启动十几秒，服务 ping 一次就走了）。
// 假 ES 是 httptest 服务：前两次请求 500，之后一律 200（ping GET / 和
// ensureIndex 的 HEAD /products 都能通过）。
func TestPingWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	setFastPingRetry(t, 5*time.Second)

	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// go-elasticsearch v8 会校验 X-Elastic-Product 响应头，裸 200 会被
		// 拒绝为 "not Elasticsearch"——假 ES 必须把这个头带上。
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if atomic.AddInt32(&count, 1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewESSearcher(srv.URL, "products", zaptest.NewLogger(t))

	// CloseClientConnections 强制断开 keep-alive 连接：ES 客户端的连接池
	// 会留着已完成请求的连接，不断开的话 srv.Close() 会干等到超时。
	defer srv.CloseClientConnections()

	require.NoError(t, err, "瞬时故障后恢复的 ES 应该等到，而不是放弃")
	// 4 次请求 = 两次失败的 ping + 第三次成功的 ping + ensureIndex 的
	// 存在性检查（HEAD /products 也吃一个 200）。
	assert.Equal(t, int32(4), atomic.LoadInt32(&count))
	_ = s
}

// TestPingWithRetry_GivesUpAfterBudget：ES 持续不可用（一直 500）时，
// 重试必须在预算内放弃并返回错误——main 靠这个错误降级 LIKE，
// 重试不能变成"永远等下去"的启动挂死。
func TestPingWithRetry_GivesUpAfterBudget(t *testing.T) {
	setFastPingRetry(t, 300*time.Millisecond)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.CloseClientConnections()
	defer srv.Close()

	_, err := NewESSearcher(srv.URL, "products", zaptest.NewLogger(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable", "错误要能看出是预算耗尽放弃")
}

// TestSearchByKeyword_LIKEMatchesDescription 是兜底能力对齐的核心用例：
// 分类埋词（「分类：手机数码」）只存在于 description，LIKE 兜底只搜 name
// 的话，降级期间分类筛选必然 0 命中（2026-09-16 事故的第二个病灶）。
func TestSearchByKeyword_LIKEMatchesDescription(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	descOnly := &model.Product{Name: "笔记本支架", PriceCents: 8900, Stock: 5,
		Description: "铝合金支架，六档高度调节。分类：手机数码。"}
	other := &model.Product{Name: "机械键盘", PriceCents: 39900, Stock: 3,
		Description: "Gasket 结构三模连接。分类：家用电器。"}
	require.NoError(t, repo.Create(context.Background(), descOnly))
	require.NoError(t, repo.Create(context.Background(), other))

	// "手机数码"不出现在任何 name 里——纯 description 命中。
	products, total, err := repo.SearchByKeyword(context.Background(), "手机数码", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, products, 1)
	assert.Equal(t, "笔记本支架", products[0].Name)
}
