import { BrowserRouter, Routes, Route, Link } from 'react-router-dom';
import { Result, Button } from 'antd';
import AppLayout from './components/AppLayout';
import RequireAuth from './components/RequireAuth';
import Home from './pages/Home';
import ProductDetail from './pages/ProductDetail';
import Login from './pages/Login';
import Register from './pages/Register';
import Orders from './pages/Orders';
import OrderDetail from './pages/OrderDetail';
import Cart from './pages/Cart';

// 路由表与"页面-接口对照表"一一对应：
//   公开：/ /products/:id /login /register
//   登录：/orders /orders/:id /cart（订单和购物车都是私密资源，与网关路由表一致）
export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="/" element={<Home />} />
          <Route path="/products/:id" element={<ProductDetail />} />
          <Route path="/login" element={<Login />} />
          <Route path="/register" element={<Register />} />
          <Route
            path="/cart"
            element={
              <RequireAuth>
                <Cart />
              </RequireAuth>
            }
          />
          <Route
            path="/orders"
            element={
              <RequireAuth>
                <Orders />
              </RequireAuth>
            }
          />
          <Route
            path="/orders/:id"
            element={
              <RequireAuth>
                <OrderDetail />
              </RequireAuth>
            }
          />
          <Route
            path="*"
            element={
              <Result
                status="404"
                title="页面不存在"
                extra={
                  <Link to="/">
                    <Button type="primary">回首页</Button>
                  </Link>
                }
              />
            }
          />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
