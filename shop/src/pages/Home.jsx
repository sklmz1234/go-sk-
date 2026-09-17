import { useEffect, useState } from 'react';
import { App, Button, Card, Col, Empty, Pagination, Row, Spin, Tag } from 'antd';
import { ShoppingCartOutlined } from '@ant-design/icons';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { addCartItem, listProducts } from '../api';
import { useSessionStore } from '../stores/session';
import { useCartStore } from '../stores/cart';
import { formatPrice } from '../utils/format';

// 4 列 × 3 行（UI 决策 1）。和后端默认值 20 故意不同——C 端卡片大，
// 一页 12 个是浏览体验最舒服的密度。
const PAGE_SIZE = 12;

// image_url 是外链，可能为空串（proto3 字段兜底）或加载失败，都要兜占位图。
function ProductCover({ product }) {
  const [broken, setBroken] = useState(false);
  if (!product.image_url || broken) {
    return (
      <div
        style={{
          height: 145,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: '#f0f0f0',
          color: '#999',
        }}
      >
        暂无图片
      </div>
    );
  }
  return (
    <img
      src={product.image_url}
      alt={product.name}
      style={{ height: 145, width: '100%', objectFit: 'cover', display: 'block' }}
      onError={() => setBroken(true)}
    />
  );
}

export default function Home() {
  // URL query 是搜索/分页状态的单一事实源：可刷新、可分享链接、浏览器后退可用。
  const [searchParams, setSearchParams] = useSearchParams();
  const keyword = searchParams.get('keyword') || '';
  const page = Math.max(1, Number(searchParams.get('page')) || 1);

  const [products, setProducts] = useState([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const navigate = useNavigate();
  const { message } = App.useApp();
  const syncCart = useCartStore((s) => s.syncFromResponse);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listProducts({ keyword, page, pageSize: PAGE_SIZE })
      .then((data) => {
        if (cancelled) return;
        setProducts(data.products || []);
        setTotal(data.total || 0);
      })
      .catch((e) => !cancelled && message.error(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // message 是 antd App 上下文的稳定引用，不进依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keyword, page]);

  function onPageChange(nextPage) {
    const params = { page: String(nextPage) };
    if (keyword) params.keyword = keyword;
    setSearchParams(params);
  }

  // 首页快捷加购（+1 件）。stopPropagation 防止触发卡片的"进详情"点击。
  // 游客 → 登录页带 redirect 回详情页（那里能看到完整信息再决定加多少）。
  async function quickAddToCart(e, product) {
    e.stopPropagation();
    if (!useSessionStore.getState().token) {
      navigate(`/login?redirect=${encodeURIComponent(`/products/${product.id}`)}`);
      return;
    }
    try {
      const resp = await addCartItem(product.id, 1);
      syncCart(resp);
      message.success(`已加入购物车：${product.name}`);
    } catch (err) {
      message.error(err.message);
    }
  }

  return (
    <Spin spinning={loading}>
      <div style={{ minHeight: 320 }}>
        {!loading && products.length === 0 ? (
          <Empty
            style={{ marginTop: 80 }}
            description={keyword ? `没有找到与「${keyword}」相关的商品` : '暂无商品'}
          />
        ) : (
          <Row gutter={[12, 12]}>
            {products.map((p) => (
              // xs 2 列 / sm 3 列 / md 起 4 列（UI 决策 1 的响应式落地）
              <Col xs={12} sm={8} md={6} key={p.id}>
                <Card
                  hoverable
                  cover={<ProductCover product={p} />}
                  onClick={() => navigate(`/products/${p.id}`)}
                >
                  <Card.Meta
                    title={p.name}
                    description={
                      <div
                        style={{
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                        }}
                      >
                        <span style={{ color: '#cf1322', fontWeight: 500 }}>
                          {formatPrice(p.price_yuan)}
                        </span>
                        {/* 列表只标有货/售罄，不给具体数字（UI 决策 4）；
                            快捷加购允许售罄商品先囤车（加购不校验库存，下单 409 兜底） */}
                        <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                          {p.stock > 0 ? <Tag color="green">有货</Tag> : <Tag>售罄</Tag>}
                          <Button
                            size="small"
                            icon={<ShoppingCartOutlined />}
                            onClick={(e) => quickAddToCart(e, p)}
                          />
                        </span>
                      </div>
                    }
                  />
                </Card>
              </Col>
            ))}
          </Row>
        )}
      </div>
      {total > PAGE_SIZE && (
        <div style={{ display: 'flex', justifyContent: 'center', marginTop: 24 }}>
          <Pagination
            current={page}
            total={total}
            pageSize={PAGE_SIZE}
            onChange={onPageChange}
            showSizeChanger={false}
          />
        </div>
      )}
    </Spin>
  );
}
