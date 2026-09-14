import { useState } from 'react';
import { App, Button, Card, Form, Input } from 'antd';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { login } from '../api';
import { useSessionStore } from '../stores/session';
import AuthPage from '../components/AuthPage';

export default function Login() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const { message } = App.useApp();
  const setSession = useSessionStore((s) => s.setSession);
  const [submitting, setSubmitting] = useState(false);

  // redirect 只允许站内路径（/ 开头且不是 // 协议跳转），防 open redirect 钓鱼。
  const raw = searchParams.get('redirect') || '/';
  const redirect = raw.startsWith('/') && !raw.startsWith('//') ? raw : '/';

  async function onFinish(values) {
    setSubmitting(true);
    try {
      const res = await login(values);
      setSession({ token: res.token, user: res.user });
      message.success(`欢迎回来，${res.user.username}`);
      // replace：登录页不该留在历史记录里，后退键不该退回登录页。
      navigate(redirect, { replace: true });
    } catch (e) {
      message.error(e.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthPage>
      <Card title="登录">
        <Form layout="vertical" onFinish={onFinish}>
          <Form.Item
            name="username"
            label="用户名"
            rules={[{ required: true, message: '请输入用户名' }]}
          >
            <Input autoFocus autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[{ required: true, message: '请输入密码' }]}
          >
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          <Form.Item style={{ marginBottom: 8 }}>
            <Button type="primary" htmlType="submit" loading={submitting} block>
              登录
            </Button>
          </Form.Item>
          <div style={{ textAlign: 'center', color: '#999' }}>
            还没有账号？
            {/* redirect 接力：注册完自动登录后还能跳回用户最初想去的页面 */}
            <Link to={redirect !== '/' ? `/register?redirect=${encodeURIComponent(redirect)}` : '/register'}>
              去注册
            </Link>
          </div>
        </Form>
      </Card>
    </AuthPage>
  );
}
