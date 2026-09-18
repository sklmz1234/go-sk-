// 搜索链路（阶段 5A）单元测试

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

// 同步双写测试

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

func TestCreate_ESFailureDoesNotBlock(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{indexErr: errors.New("es: connection refused")}
	p := &model.Product{Name: "蓝牙耳机"}
	next.EXPECT().Create(mock.Anything, p).Return(nil)

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	require.NoError(t, repo.Create(context.Background(), p), "ES 双写失败不能让 Create 失败")
	assert.Len(t, searcher.indexed, 1, "索引动作确实尝试过")
}

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

func TestESSearcher_SlowESFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

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

func TestSearchByKeyword_EmptyRecall(t *testing.T) {
	next := mocks.NewMockRepository(t)
	searcher := &fakeSearcher{ids: []uint64{}, total: 0}

	repo := NewSearchRepository(next, searcher, zaptest.NewLogger(t))
	products, total, err := repo.SearchByKeyword(context.Background(), "不存在的商品", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, products)
}

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

func TestSearchByKeyword_LIKEEscapesWildcards(t *testing.T) {
	repo := NewGormRepository(newSQLiteDB(t))
	seedProduct(t, repo, "蓝牙耳机", 19900, 10)
	seedProduct(t, repo, "机械键盘", 39900, 3)

	products, total, err := repo.SearchByKeyword(context.Background(), "100%", 1, 20)

	require.NoError(t, err)
	assert.Equal(t, int64(0), total, "% 必须按字面量匹配，不能命中任何商品")
	assert.Empty(t, products)
}

// TestSearchByKeyword_LIKEEscapeCharItself 转义符（!）自身必须按字面量匹配

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

// —— 2026-09-16 分类搜索事故的回归测试对

func setFastPingRetry(t *testing.T, maxWait time.Duration) {
	t.Helper()
	origWait, origMin, origMax, origSleep := esPingMaxWait, esPingMinDelay, esPingMaxDelay, esPingSleep
	t.Cleanup(func() {
		esPingMaxWait, esPingMinDelay, esPingMaxDelay, esPingSleep = origWait, origMin, origMax, origSleep
	})
	esPingMaxWait = maxWait
	esPingSleep = func(time.Duration) {}
}

func TestPingWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	setFastPingRetry(t, 5*time.Second)

	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {

		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if atomic.AddInt32(&count, 1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewESSearcher(srv.URL, "products", zaptest.NewLogger(t))

	defer srv.CloseClientConnections()

	require.NoError(t, err, "瞬时故障后恢复的 ES 应该等到，而不是放弃")

	assert.Equal(t, int32(4), atomic.LoadInt32(&count))
	_ = s
}

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
