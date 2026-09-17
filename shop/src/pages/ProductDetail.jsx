import { useEffect, useState } from 'react';
import { App, Button, Col, InputNumber, Modal, Row, Spin, Tag } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';
import { addCartItem, createOrder, getProduct } from '../api';
import { useSessionStore } from '../stores/session';
import { useCartStore } from '../stores/cart';
import { formatPrice } from '../utils/format';

export default function ProductDetail() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { message } = App.useApp();
  const token = useSessionStore((s) => s.token);
  const syncCart = useCartStore((s) => s.syncFromResponse);

  const [product, setProduct] = useState(null);
  const [loading, setLoading] = useState(true);

  // 详情页的数量只是初值，弹窗里还能再改（UI 决策 3），所以两个状态分开。
  const [qty, setQty] = useState(1);
  const [modalOpen, setModalOpen] = useState(false);
  const [modalQty, setModalQty] = useState(1);
  const [submitting, setSubmitting] = useState(false);
  const [adding, setAdding] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getProduct(id)
      .then((data) => !cancelled && setProduct(data))
      .catch((e) => !cancelled && message.error(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  function openBuyModal() {
    // 未登录点"立即购买"→ 登录页带 redirect，登录成功自动回本页（验收第 3 条）。
    if (!token) {
      navigate(`/login?redirect=${encodeURIComponent(`/products/${id}`)}`);
      return;
    }
    setModalQty(qty);
    setModalOpen(true);
  }

  // 加入购物车：和"立即购买"共用未登录跳转（redirect 回本页）。
  // 成功后用响应里的 total_quantity 就地刷新顶栏角标——后端所有购物车
  // 接口都带回最新全量，这是 5B 的接口契约红利。
  async function addToCart() {
    if (!token) {
      navigate(`/login?redirect=${encodeURIComponent(`/products/${id}`)}`);
      return;
    }
    setAdding(true);
    try {
      const resp = await addCartItem(product.id, qty);
      syncCart(resp);
      message.success(`已加入购物车（当前共 ${resp.total_quantity} 件）`);
    } catch (e) {
      message.error(e.message);
    } finally {
      setAdding(false);
    }
  }

  async function submitOrder() {
    setSubmitting(true);
    try {
      const order = await createOrder([{ product_id: product.id, quantity: modalQty }]);
      message.success('下单成功');
      setModalOpen(false);
      navigate(`/orders/${order.id}`);
    } catch (e) {
      // 409 库存不足等服务端错误在这里可读提示（验收第 4 条），
      // 弹窗保持打开，用户可以改小数量重试。
      message.error(e.message);
    } finally {
      setSubmitting(false);
    }
  }

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }
  if (!product) return null;

  const soldOut = product.stock <= 0;

  return (
    <Row gutter={[24, 24]}>
      {/* 窄屏（xs）上下堆叠，md 起左图右信息 */}
      <Col xs={24} md={10}>
        {product.image_url ? (
          <img
            src={product.image_url}
            alt={product.name}
            style={{ width: '100%', borderRadius: 8, display: 'block' }}
          />
        ) : (
          <div
            style={{
              width: '100%',
              height: 320,
              borderRadius: 8,
              background: '#f0f0f0',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              color: '#999',
            }}
          >
            暂无图片
          </div>
        )}
      </Col>
      <Col xs={24} md={14}>
        <h1 style={{ fontSize: 22, margin: '0 0 12px' }}>{product.name}</h1>
        <p style={{ color: '#666', lineHeight: 1.8, minHeight: 48 }}>
          {product.description || '这个商品还没有描述。'}
        </p>
        <div style={{ fontSize: 28, color: '#cf1322', fontWeight: 500, margin: '12px 0' }}>
          {formatPrice(product.price_yuan)}
        </div>
        {/* 详情页给具体库存数（UI 决策 4），秒杀场景下它只是参考——以服务端扣减为准 */}
        <div style={{ marginBottom: 20 }}>
          {soldOut ? <Tag>售罄</Tag> : <span style={{ color: '#666' }}>库存 {product.stock} 件</span>}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <InputNumber
            min={1}
            max={Math.max(1, product.stock)}
            value={qty}
            onChange={(v) => setQty(v || 1)}
            disabled={soldOut}
          />
          <Button type="primary" size="large" disabled={soldOut} onClick={openBuyModal}>
            {soldOut ? '已售罄' : '立即购买'}
          </Button>
          {/* 加购不校验库存（下单 409 兜底），售罄也可先囤在车里等补货 */}
          <Button size="large" loading={adding} onClick={addToCart}>
            加入购物车
          </Button>
        </div>
      </Col>

      <Modal
        title="确认订单"
        open={modalOpen}
        onOk={submitOrder}
        onCancel={() => setModalOpen(false)}
        okText="提交订单"
        cancelText="再想想"
        confirmLoading={submitting}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', margin: '16px 0' }}>
          <span>{product.name}</span>
          <span>{formatPrice(product.price_yuan)}</span>
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span>数量</span>
          <InputNumber
            min={1}
            max={Math.max(1, product.stock)}
            value={modalQty}
            onChange={(v) => setModalQty(v || 1)}
          />
        </div>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            marginTop: 16,
            fontWeight: 500,
          }}
        >
          <span>合计</span>
          <span style={{ color: '#cf1322' }}>{formatPrice(product.price_yuan * modalQty)}</span>
        </div>
      </Modal>
    </Row>
  );
}
