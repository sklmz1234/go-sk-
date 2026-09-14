import { Layout, Input, Button, Dropdown, Space } from 'antd';
import { Link, Outlet, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { useSessionStore } from '../stores/session';

const { Header, Content, Footer } = Layout;

// 顶栏：logo / 搜索框 / 登录态。
// 搜索框不持有结果，只负责把 keyword 写进首页的 URL query（/?keyword=xxx），
// 真正的查询由首页根据 URL 发起——keyword 进 URL 才能保证刷新/分享后结果一致。
export default function AppLayout() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const keyword = searchParams.get('keyword') || '';

  const token = useSessionStore((s) => s.token);
  const user = useSessionStore((s) => s.user);
  const clearSession = useSessionStore((s) => s.clearSession);

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
    navigate('/');
  };

  const userMenu = {
    items: [
      { key: 'orders', label: '我的订单' },
      { key: 'logout', label: '退出登录', danger: true },
    ],
    onClick: ({ key }) => {
      if (key === 'orders') navigate('/orders');
      if (key === 'logout') handleLogout();
    },
  };

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
