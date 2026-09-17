import { useEffect } from 'react';
import { Badge, Button, Carousel, Dropdown, Input, Layout, Space, Tooltip } from 'antd';
import { DownOutlined, ShoppingCartOutlined } from '@ant-design/icons';
import { Link, Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { useSessionStore } from '../stores/session';
import { useCartStore } from '../stores/cart';

const { Header, Content, Footer } = Layout;

// 5 个分类（与 seed 数据里 description 埋的「分类：xxx」一一对应）。
// 点分类 = 搜分类名：商品没有独立的 category 字段（后端契约不变），
// 分类筛选复用 keyword 搜索链路（ES multi_match 命中描述里的埋词）。
const CATEGORIES = ['手机数码', '家用电器', '运动户外', '美妆个护', '休闲零食'];

// hero 轮播图：picsum 外链占位（和 seed 商品图同一套思路），纯展示无交互。
const HERO_SLIDES = [
  'https://picsum.photos/seed/hero-tech/1200/500',
  'https://picsum.photos/seed/hero-home/1200/500',
  'https://picsum.photos/seed/hero-outdoor/1200/500',
  'https://picsum.photos/seed/hero-beauty/1200/500',
];

// HeroBanner：紧贴 Header 下方的全宽黑色品牌区。放在 AppLayout 里、
// Content 之外——Content 有 maxWidth 1200 的容器，hero 要通到屏幕
// 左右两边就不能进它。只在纯首页（'/' 且无 keyword）渲染：搜索/
// 分类筛选时首屏让位给结果。
function HeroBanner() {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        background: '#000',
        marginBottom: 20,
        // 左右 padding 随屏宽伸缩：窄屏不挤、宽屏文字不至于飘到屏幕正中。
        padding: '56px clamp(32px, 6vw, 96px)',
        gap: 'clamp(32px, 5vw, 96px)',
      }}
    >
      {/* 左：品牌标语 */}
      <div style={{ flexShrink: 0 }}>
        <div style={{ color: '#fff', fontSize: 44, fontWeight: 700, lineHeight: 1.2 }}>
          sk优选
        </div>
        <div style={{ color: 'rgba(255,255,255,0.65)', fontSize: 14, marginTop: 12 }}>
          精选全球好物 · 打造理想生活
        </div>
        <div
          style={{
            color: 'rgba(255,255,255,0.45)',
            fontSize: 12,
            marginTop: 36,
            letterSpacing: 4,
          }}
        >
          正品保障 · 精选好物 · 极速送达
        </div>
        {/* 免责小字：个人学习项目声明，防止截图/分享时被当成真实商城 */}
        <div
          style={{
            color: 'rgba(255,255,255,0.3)',
            fontSize: 12,
            marginTop: 10,
            maxWidth: 360,
            lineHeight: 1.6,
          }}
        >
          本站点为个人学习项目，用于 Go 后端开发技术实践与面试作品展示。主要功能为模拟商品展示、订单流程等电商场景，非真实业务。
        </div>
      </div>
      {/* 右：轮播图（flex:1 吃满剩余宽度，全宽 hero 下自然比容器版大得多；
          height 固定保证不同尺寸图不跳动） */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <Carousel autoplay autoplaySpeed={4000}>
          {HERO_SLIDES.map((url) => (
            <div key={url}>
              <img
                src={url}
                alt="sk优选精选好物"
                style={{
                  width: '100%',
                  height: 300,
                  objectFit: 'cover',
                  borderRadius: 8,
                  display: 'block',
                }}
              />
            </div>
          ))}
        </Carousel>
      </div>
    </div>
  );
}

// 顶栏：logo / 搜索框 / 购物车角标 / 登录态。
// 搜索框不持有结果，只负责把 keyword 写进首页的 URL query（/?keyword=xxx），
// 真正的查询由首页根据 URL 发起——keyword 进 URL 才能保证刷新/分享后结果一致。
export default function AppLayout() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const keyword = searchParams.get('keyword') || '';

  const token = useSessionStore((s) => s.token);
  const user = useSessionStore((s) => s.user);
  const clearSession = useSessionStore((s) => s.clearSession);

  const totalQuantity = useCartStore((s) => s.totalQuantity);
  const refreshCart = useCartStore((s) => s.refresh);
  const clearCart = useCartStore((s) => s.clear);

  // token 变化（登录/退出/刷新）时同步角标：登录 → 拉真实数量；
  // 退出 → 本地清零（useCartStore.refresh 内部也会兜，这里显式保证语义）。
  useEffect(() => {
    if (token) {
      refreshCart();
    } else {
      clearCart();
    }
  }, [token, refreshCart, clearCart]);

  // 登录/注册页是"账号流程"不是"购物流程"：顶栏藏搜索框，
  // 内容区也让出 padding/maxWidth，给 AuthPage 的装饰背景整幅画布。
  const location = useLocation();
  const isAuthPage = location.pathname === '/login' || location.pathname === '/register';

  const handleSearch = (value) => {
    const kw = value.trim();
    navigate(kw ? `/?keyword=${encodeURIComponent(kw)}` : '/');
  };

  const handleLogout = () => {
    clearSession();
    clearCart();
    navigate('/');
  };

  const userMenu = {
    items: [
      { key: 'orders', label: '我的订单' },
      { key: 'cart', label: '购物车' },
      { key: 'logout', label: '退出登录', danger: true },
    ],
    onClick: ({ key }) => {
      if (key === 'orders') navigate('/orders');
      if (key === 'cart') navigate('/cart');
      if (key === 'logout') handleLogout();
    },
  };

  // 导航高亮：当前在哪，哪个按钮就实心白字。
  // 首页 = 首页路由且无关键词；分类 = URL keyword 恰好是某个分类名
  // （搜索框此时也显示分类名，两处状态天然一致，因为都来自同一个 URL）；
  // 我的 = 订单/购物车页（"我的"承载账号相关的所有页面）。
  const isHomeActive = location.pathname === '/' && !keyword;
  const isCategoryActive = CATEGORIES.includes(keyword);
  const isMineActive = location.pathname === '/orders' || location.pathname === '/cart';

  // 顶栏导航按钮的样式（深蓝 Header 上的白字文字按钮，active 实心加粗）。
  // 注意这是个返回 style 的普通函数而不是包装组件：Dropdown 的子元素必须是
  // antd 组件（Button 自带 forwardRef），rc-dropdown 会往子元素注入 hover/click
  // 触发事件和定位弹层用的 ref——自定义函数组件接不住这些，下拉会没反应
  // （用户名那组能用正是因为 child 是 forwardRef 的 Space）。
  const navButtonStyle = (active) => ({
    color: active ? '#fff' : 'rgba(255, 255, 255, 0.65)',
    fontWeight: active ? 600 : 400,
    paddingInline: 12,
  });

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '0 16px' }}>
        <Link to="/" style={{ color: '#fff', fontSize: 18, fontWeight: 600, whiteSpace: 'nowrap' }}>
          sk商城
        </Link>
        {/* key=keyword：URL 里的关键词变化时强制重建输入框，保证框内文字与 URL 同步。
            flex:1 + minWidth:0 让搜索框在窄屏下压缩而不是把登录按钮挤出屏幕。
            登录/注册页不渲染搜索框——账号流程里没有购物搜索的语境。 */}
        {!isAuthPage && (
          <Input.Search
            key={keyword}
            defaultValue={keyword}
            placeholder="搜索商品"
            allowClear
            onSearch={handleSearch}
            style={{ flex: 1, maxWidth: 420, minWidth: 0 }}
          />
        )}
        {/* 导航条（首页/分类/我的）：紧跟搜索框，是 Header 上第二组交互。
            点"分类"下拉出 5 个分类，跳 /?keyword=分类名 —— 和手动在搜索框
            输入分类名完全等价（同一个 URL 状态源），所以搜索框会同步显示
            分类名、Empty 文案、分页也全部复用搜索的既有行为，零额外逻辑。 */}
        {!isAuthPage && (
          <Space size={4} style={{ whiteSpace: 'nowrap' }}>
            <Button type="text" style={navButtonStyle(isHomeActive)} onClick={() => navigate('/')}>
              首页
            </Button>
            {/* child 必须是 antd Button 本体（见 navButtonStyle 的注释）。
                trigger 悬停/点击都能弹：桌面用户习惯 hover，触屏用户靠点。 */}
            <Dropdown
              menu={{
                items: CATEGORIES.map((c) => ({ key: c, label: c })),
                onClick: ({ key }) => navigate(`/?keyword=${encodeURIComponent(key)}`),
              }}
              trigger={['hover', 'click']}
            >
              <Button type="text" style={navButtonStyle(isCategoryActive)}>
                分类 <DownOutlined style={{ fontSize: 10, marginLeft: 2 }} />
              </Button>
            </Dropdown>
            <Button
              type="text"
              style={navButtonStyle(isMineActive)}
              onClick={() => navigate(token ? '/orders' : '/login')}
            >
              我的
            </Button>
          </Space>
        )}
        {/* 购物车入口紧跟搜索框：浏览 → 搜索 → 加购 → 结算是 C 端主路径。
            游客也可见（点进 /cart 会被 RequireAuth 拦去登录，和"立即购买"一致）。 */}
        {!isAuthPage && (
          <Tooltip title="购物车">
            <Badge count={totalQuantity} size="small" offset={[-2, 2]}>
              <Button
                type="text"
                icon={<ShoppingCartOutlined style={{ color: '#fff', fontSize: 18 }} />}
                onClick={() => navigate('/cart')}
              />
            </Badge>
          </Tooltip>
        )}
        <div style={{ marginLeft: 'auto' }}>
          {token ? (
            <Dropdown menu={userMenu}>
              <Space style={{ color: '#fff', cursor: 'pointer' }}>{user?.username}</Space>
            </Dropdown>
          ) : (
            <Space>
              <Button type="link" style={{ color: '#fff' }} onClick={() => navigate('/login')}>
                登录
              </Button>
              <Button ghost onClick={() => navigate('/register')}>
                注册
              </Button>
            </Space>
          )}
        </div>
      </Header>
      {/* 全宽黑色品牌区：贴屏幕左右边（Content 的 1200 容器外），
          仅纯首页展示——有 keyword 说明用户在搜索/筛选，要的是结果。 */}
      {location.pathname === '/' && !keyword && <HeroBanner />}
      {/* 登录/注册页让 Content 变成无约束的 flex 容器，AuthPage（flex:1）才能
          撑满整个内容区，装饰背景不会被 1200px 最大宽度截断 */}
      <Content
        style={
          isAuthPage
            ? { padding: 0, display: 'flex', flexDirection: 'column' }
            : { padding: '24px', maxWidth: 1200, width: '100%', margin: '0 auto' }
        }
      >
        <Outlet />
      </Content>
      <Footer style={{ textAlign: 'center', color: '#999' }}>
        sk商城 · 个人学习项目
      </Footer>
    </Layout>
  );
}
