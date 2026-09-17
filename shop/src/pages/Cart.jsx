import { useEffect, useState } from 'react';
import {
  App,
  Button,
  Card,
  Checkbox,
  Empty,
  InputNumber,
  Modal,
  Popconfirm,
  Space,
  Spin,
  Tag,
} from 'antd';
import { EnvironmentOutlined, RedoOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { createOrder, getRandomAddress, listCart, removeCartItems, updateCartItem } from '../api';
import { useCartStore } from '../stores/cart';
import { formatPrice } from '../utils/format';

// 购物车页（阶段 5B）：列表（勾选/改数量/删除）+ 底栏合计 + 去结算。
// 数据全部来自 GET /cart（后端持久化的验证点：刷新/换浏览器数据不丢），
// 变更操作直接用响应里带回的最新全量就地刷新，不重复 GET。

// 小图兜底：image_url 为空或加载失败都显示占位块（同首页 ProductCover 语义）。
function CartThumb({ item }) {
  const [broken, setBroken] = useState(false);
  if (!item.image_url || broken) {
    return (
      <div
        style={{
          width: 72,
          height: 72,
          borderRadius: 6,
          background: '#f0f0f0',
          color: '#999',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          fontSize: 12,
        }}
      >
        暂无图片
      </div>
    );
  }
  return (
    <img
      src={item.image_url}
      alt={item.name}
      onError={() => setBroken(true)}
      style={{ width: 72, height: 72, objectFit: 'cover', borderRadius: 6, display: 'block' }}
    />
  );
}

export default function Cart() {
  const navigate = useNavigate();
  const { message } = App.useApp();
  const syncCart = useCartStore((s) => s.syncFromResponse);

  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState([]); // 勾选的 product_id 集合
  const [settling, setSettling] = useState(false); // 结算提交中
  const [confirmOpen, setConfirmOpen] = useState(false); // 结算确认弹窗
  const [address, setAddress] = useState(null); // 随机收货地址（弹窗内展示）
  const [addressLoading, setAddressLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listCart()
      .then((resp) => {
        if (cancelled) return;
        setItems(resp.items || []);
        syncCart(resp);
      })
      .catch((e) => !cancelled && message.error(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 商品已删（LEFT JOIN 补零值：name 空 + stock 0）——展示"已下架"，禁止勾选。
  const isGhost = (item) => !item.name && item.stock <= 0;

  const selectableIds = items.filter((it) => !isGhost(it)).map((it) => it.product_id);
  const allSelected = selectableIds.length > 0 && selectableIds.every((id) => selected.includes(id));

  // 合计只算勾选项：普通项按"数量 × 单价"，售罄项单价照算（下单时 409 兜底，
  // 用户在结算失败前就能看到金额，不是隐藏信息）。
  const selectedItems = items.filter((it) => selected.includes(it.product_id));
  const totalPrice = selectedItems.reduce((sum, it) => sum + it.price_yuan * it.quantity, 0);

  const toggleSelect = (id, checked) => {
    setSelected((prev) => (checked ? [...prev, id] : prev.filter((x) => x !== id)));
  };

  const toggleSelectAll = (checked) => {
    setSelected(checked ? selectableIds : []);
  };

  function applyResponse(resp) {
    setItems(resp.items || []);
    syncCart(resp);
    // 已删条目从勾选集合里清掉，防止"勾选了一个刚被删的行"的悬空 id。
    const alive = new Set((resp.items || []).map((it) => it.product_id));
    setSelected((prev) => prev.filter((id) => alive.has(id)));
  }

  async function changeQty(item, quantity) {
    try {
      const resp = await updateCartItem(item.product_id, quantity);
      applyResponse(resp);
    } catch (e) {
      message.error(e.message);
    }
  }

  async function removeOne(item) {
    try {
      const resp = await removeCartItems([item.product_id]);
      applyResponse(resp);
      message.success('已删除');
    } catch (e) {
      message.error(e.message);
    }
  }

  // 结算分两步：点"去结算"先弹确认窗（拉一条随机地址展示），用户确认后才真正下单。
  // 地址随弹窗每次重新随机（换一条按钮也走这里）——mock 池的乐趣就在随机。
  async function openConfirm() {
    if (selectedItems.length === 0) return;
    setConfirmOpen(true);
    await rollAddress();
  }

  async function rollAddress() {
    setAddressLoading(true);
    try {
      setAddress(await getRandomAddress());
    } catch (e) {
      setAddress(null);
      // 地址是展示性数据，取不到不阻塞结算，但要让用户知道。
      message.warning(`收货地址获取失败：${e.message}`);
    } finally {
      setAddressLoading(false);
    }
  }

  // 确认结算（前端编排，5B 决策 2）：
  //   勾选项组 items[] → 复用现有 POST /orders（下单链路零改动）
  //   → 201 成功 → DELETE /cart/items 清勾选项（幂等，失败可重试不资损）
  //   → 跳订单详情（PENDING 状态，接下来走"模拟支付"）。
  // 409（库存不足）：可读提示，弹窗不关、购物车不动，可取消勾选超卖项再试。
  async function settle() {
    if (selectedItems.length === 0) return;
    setSettling(true);
    try {
      const order = await createOrder(
        selectedItems.map((it) => ({ product_id: it.product_id, quantity: it.quantity })),
      );
      // 下单成功才清车；清车失败不影响订单（幂等可重试，残留手动删）。
      try {
        const resp = await removeCartItems(selectedItems.map((it) => it.product_id));
        applyResponse(resp);
      } catch {
        message.warning('订单已生成，但购物车清理失败，请手动删除已购条目');
      }
      const addrText = address
        ? `，将发往：${address.receiver_name} ${address.phone}`
        : '';
      message.success(`下单成功${addrText}，请完成支付`);
      setConfirmOpen(false);
      navigate(`/orders/${order.id}`);
    } catch (e) {
      // 409 库存不足等：可读提示，购物车数据不动（验收第 6 条）。
      message.error(e.message);
    } finally {
      setSettling(false);
    }
  }

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  if (items.length === 0) {
    return <Empty style={{ marginTop: 80 }} description="购物车是空的，去逛逛吧" />;
  }

  return (
    <div>
      <h2 style={{ marginTop: 0 }}>购物车</h2>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        {items.map((item) => {
          const ghost = isGhost(item);
          const soldOut = !ghost && item.stock <= 0;
          const checked = selected.includes(item.product_id);
          return (
            <Card key={item.product_id} styles={{ body: { padding: 16 } }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                <Checkbox
                  checked={checked}
                  disabled={ghost}
                  onChange={(e) => toggleSelect(item.product_id, e.target.checked)}
                />
                <CartThumb item={item} />
                <div
                  style={{ flex: 1, minWidth: 0, cursor: 'pointer' }}
                  onClick={() => !ghost && navigate(`/products/${item.product_id}`)}
                >
                  <div style={{ fontWeight: 500, marginBottom: 4 }}>
                    {ghost ? <Tag>已下架</Tag> : item.name}
                  </div>
                  <Space size={8}>
                    <span style={{ color: '#cf1322', fontWeight: 500 }}>
                      {formatPrice(item.price_yuan)}
                    </span>
                    {soldOut && <Tag>售罄</Tag>}
                  </Space>
                </div>
                {/* 改数量：InputNumber 失焦/回车才提交（onChange 的值先暂存，
                    onBlur 时才调接口），避免每次点箭头都打一次 PUT */}
                <QtyEditor item={item} disabled={ghost} onCommit={changeQty} />
                <span style={{ width: 88, textAlign: 'right', color: '#cf1322', fontWeight: 500 }}>
                  {formatPrice(item.price_yuan * item.quantity)}
                </span>
                <Popconfirm title="确定删除这个商品吗？" onConfirm={() => removeOne(item)}>
                  <Button type="link" danger disabled={ghost}>
                    删除
                  </Button>
                </Popconfirm>
              </div>
            </Card>
          );
        })}
      </Space>

      {/* 底栏：全选 + 合计 + 去结算 */}
      <Card
        style={{ marginTop: 16 }}
        styles={{ body: { padding: '12px 16px' } }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
          <Checkbox
            checked={allSelected}
            onChange={(e) => toggleSelectAll(e.target.checked)}
            disabled={selectableIds.length === 0}
          >
            全选
          </Checkbox>
          <span style={{ color: '#999' }}>已选 {selected.length} 项</span>
          <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 16 }}>
            <span>
              合计：
              <span style={{ color: '#cf1322', fontSize: 18, fontWeight: 500 }}>
                {formatPrice(totalPrice)}
              </span>
            </span>
            <Button
              type="primary"
              size="large"
              disabled={selected.length === 0}
              onClick={openConfirm}
            >
              去结算
            </Button>
          </div>
        </div>
      </Card>

      {/* 结算确认弹窗：收货地址（随机，可换）+ 商品清单 + 合计。
          地址是 5B 的 mock 池——没有真实地址簿，随机一条让结算有完整的
          视觉闭环；"换一条"重掷随机数，取不到地址也不阻塞下单。 */}
      <Modal
        title="确认订单"
        open={confirmOpen}
        onOk={settle}
        onCancel={() => setConfirmOpen(false)}
        okText={`提交订单（${formatPrice(totalPrice)}）`}
        cancelText="再想想"
        confirmLoading={settling}
      >
        {addressLoading ? (
          <div style={{ textAlign: 'center', padding: 12 }}>
            <Spin />
          </div>
        ) : address ? (
          <Card size="small" style={{ marginBottom: 16 }} styles={{ body: { padding: 12 } }}>
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
              <EnvironmentOutlined style={{ color: '#cf1322', marginTop: 3 }} />
              <div style={{ flex: 1 }}>
                <div style={{ fontWeight: 500, marginBottom: 2 }}>
                  {address.receiver_name}　{address.phone}
                </div>
                <div style={{ color: '#666' }}>{address.address}</div>
              </div>
              <Button type="link" size="small" icon={<RedoOutlined />} onClick={rollAddress}>
                换一条
              </Button>
            </div>
          </Card>
        ) : (
          <div style={{ color: '#999', marginBottom: 16 }}>
            未获取到收货地址（不影响下单）
            <Button type="link" size="small" onClick={rollAddress}>
              重试
            </Button>
          </div>
        )}
        {selectedItems.map((it) => (
          <div
            key={it.product_id}
            style={{ display: 'flex', justifyContent: 'space-between', margin: '6px 0' }}
          >
            <span>
              {it.name} × {it.quantity}
            </span>
            <span>{formatPrice(it.price_yuan * it.quantity)}</span>
          </div>
        ))}
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            marginTop: 12,
            paddingTop: 8,
            borderTop: '1px solid #f0f0f0',
            fontWeight: 500,
          }}
        >
          <span>合计</span>
          <span style={{ color: '#cf1322' }}>{formatPrice(totalPrice)}</span>
        </div>
      </Modal>
    </div>
  );
}

// QtyEditor：数量编辑器。InputNumber 的受控值先落在本地 state，
// onBlur（失焦/回车后）才把终值提交给父组件调 PUT——连续点 +/- 不会
// 每次都打接口，也就不会和后端"绝对值语义"打架产生中间态抖动。
function QtyEditor({ item, disabled, onCommit }) {
  const [value, setValue] = useState(item.quantity);
  // 外部数据变化（如结算清车后的响应刷新）同步回本地。
  useEffect(() => {
    setValue(item.quantity);
  }, [item.quantity]);

  return (
    <InputNumber
      min={1}
      max={99}
      value={value}
      disabled={disabled}
      onChange={(v) => setValue(v || 1)}
      onBlur={() => {
        if (value !== item.quantity) onCommit(item, value);
      }}
      onPressEnter={(e) => e.target.blur()}
      style={{ width: 72 }}
    />
  );
}
