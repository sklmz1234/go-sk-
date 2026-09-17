import { useEffect, useState } from 'react';
import { App, Button, Card, Descriptions, Modal, Popconfirm, Spin, Table, Tag } from 'antd';
import { useNavigate, useParams } from 'react-router-dom';
import { cancelOrder, getOrder, payOrder } from '../api';
import { formatDate, formatPrice } from '../utils/format';

const STATUS_TAG = {
  PENDING: { color: 'orange', text: '待支付' },
  PAID: { color: 'green', text: '已支付' },
  CANCELLED: { color: 'default', text: '已取消' },
};

// 订单详情是唯一带 items 的接口（列表靠 omitempty 共用 DTO 不带），
// items 里的商品名和单价是下单时刻的快照——商品以后改名改价不影响这里。
//
// 阶段 5B 模拟支付：PENDING 订单显示"立即支付"，弹窗确认后 POST pay
// （后端条件更新 PENDING → PAID），成功后就地刷新状态。
export default function OrderDetail() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { message } = App.useApp();
  const [order, setOrder] = useState(null);
  const [loading, setLoading] = useState(true);
  const [payOpen, setPayOpen] = useState(false);
  const [paying, setPaying] = useState(false);
  const [cancelling, setCancelling] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getOrder(id)
      .then((data) => !cancelled && setOrder(data))
      .catch((e) => !cancelled && message.error(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }
  if (!order) return null;

  const status = STATUS_TAG[order.status] || { color: 'default', text: order.status };
  const isPending = order.status === 'PENDING';

  // 模拟支付：学习项目没有支付渠道，"支付成功"= 一次 PENDING → PAID 的
  // 状态迁移（后端条件更新，并发安全）。409 提示订单状态已变（比如刚被取消）。
  async function confirmPay() {
    setPaying(true);
    try {
      const updated = await payOrder(order.id);
      setOrder(updated);
      setPayOpen(false);
      message.success('支付成功（模拟）');
    } catch (e) {
      message.error(e.message);
      // 409（已支付/已取消/与取消赛跑）后拉最新状态，别让页面停在旧状态上。
      try {
        setOrder(await getOrder(order.id));
      } catch {
        /* 刷新失败就保持现状，用户手动刷新 */
      }
    } finally {
      setPaying(false);
    }
  }

  async function handleCancel() {
    setCancelling(true);
    try {
      const updated = await cancelOrder(order.id);
      setOrder(updated);
      message.success('订单已取消，库存将自动回补');
    } catch (e) {
      message.error(e.message);
    } finally {
      setCancelling(false);
    }
  }

  const itemColumns = [
    { title: '商品', dataIndex: 'product_name' },
    { title: '数量', dataIndex: 'quantity', render: (q) => `×${q}` },
    {
      title: '单价（下单快照）',
      dataIndex: 'unit_price_yuan',
      render: (v) => formatPrice(v),
    },
    {
      title: '小计',
      key: 'subtotal',
      render: (_, item) => formatPrice(item.unit_price_yuan * item.quantity),
    },
  ];

  return (
    <Card
      title={`订单 #${order.id}`}
      extra={
        <>
          {isPending && (
            <Popconfirm
              title="确定取消这个订单吗？"
              description="已扣的库存会自动回补"
              onConfirm={handleCancel}
            >
              <Button danger loading={cancelling} style={{ marginRight: 8 }}>
                取消订单
              </Button>
            </Popconfirm>
          )}
          <Button onClick={() => navigate('/orders')}>返回列表</Button>
        </>
      }
    >
      <Descriptions column={{ xs: 1, sm: 2 }} style={{ marginBottom: 24 }}>
        <Descriptions.Item label="状态">
          <Tag color={status.color}>{status.text}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="下单时间">{formatDate(order.created_at)}</Descriptions.Item>
        <Descriptions.Item label="订单总额">
          <span style={{ color: '#cf1322', fontWeight: 500 }}>{formatPrice(order.total_yuan)}</span>
        </Descriptions.Item>
      </Descriptions>
      <Table
        rowKey="product_id"
        columns={itemColumns}
        dataSource={order.items || []}
        pagination={false}
      />

      {/* 待支付订单的操作条：模拟支付是 5B 的最后一环，下单 → 支付全链路闭环 */}
      {isPending && (
        <div style={{ marginTop: 24, textAlign: 'right' }}>
          <Button type="primary" size="large" onClick={() => setPayOpen(true)}>
            立即支付
          </Button>
        </div>
      )}

      <Modal
        title="模拟支付"
        open={payOpen}
        onOk={confirmPay}
        onCancel={() => setPayOpen(false)}
        okText={`确认支付 ${formatPrice(order.total_yuan)}`}
        cancelText="再想想"
        confirmLoading={paying}
      >
        <p style={{ color: '#666' }}>
          本项目为学习项目，不接入真实支付渠道——点击确认即视为支付成功，
          订单状态将从「待支付」变为「已支付」。
        </p>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            fontWeight: 500,
          }}
        >
          <span>支付金额</span>
          <span style={{ color: '#cf1322', fontSize: 18 }}>{formatPrice(order.total_yuan)}</span>
        </div>
      </Modal>
    </Card>
  );
}
