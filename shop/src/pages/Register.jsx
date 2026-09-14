import { useState } from 'react';
import { App, Button, Card, Form, Input } from 'antd';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { login, register } from '../api';
import { useSessionStore } from '../stores/session';
import AuthPage from '../components/AuthPage';

// 校验规则对齐后端 RegisterRequest 的 binding（dto.go:19-23）：
// username required、email 格式、password min=6。前端先拦一道，体验比等 400 好。
export default function Register() {
  const navigate = useNavigate();
  const { message } = App.useApp();
  const setSession = useSessionStore((s) => s.setSession);
  const [submitting, setSubmitting] = useState(false);

  // 和登录页同款：redirect 只允许站内路径，防 open redirect。
  const [searchParams] = useSearchParams();
  const raw = searchParams.get('redirect') || '/';
  const redirect = raw.startsWith('/') && !raw.startsWith('//') ? raw : '/';

  async function onFinish(values) {
    setSubmitting(true);
    try {
      await register({
        username: values.username,
        email: values.email,
        password: values.password,
      });
      // 注册接口只返回 user 不返 token（契约如此）。紧接着用同一组凭证
      // 调一次登录接口拿 token——"注册完就是登录态"是 C 端的基本预期，
      // 不该让用户在登录页把刚输过的账号密码再敲一遍。
      try {
        const res = await login({ username: values.username, password: values.password });
        setSession({ token: res.token, user: res.user });
        message.success(`注册成功，欢迎，${res.user.username}`);
        navigate(redirect, { replace: true });
      } catch {
        // 自动登录失败（罕见，如限流）不吞掉注册成果：账号已建好，退回手动登录。
        message.success('注册成功，请登录');
        navigate('/login');
      }
    } catch (e) {
      message.error(e.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthPage>
      <Card title="注册">
        <Form layout="vertical" onFinish={onFinish}>
          <Form.Item
            name="username"
            label="用户名"
            rules={[{ required: true, message: '请输入用户名' }]}
          >
            <Input autoFocus autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="email"
            label="邮箱"
            rules={[
              { required: true, message: '请输入邮箱' },
              { type: 'email', message: '邮箱格式不正确' },
            ]}
          >
            <Input autoComplete="email" />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[
              { required: true, message: '请输入密码' },
              { min: 6, message: '密码至少 6 位' },
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm"
            label="确认密码"
            dependencies={['password']}
            rules={[
              { required: true, message: '请再输入一次密码' },
              ({ getFieldValue }) => ({
                validator: (_, value) =>
                  !value || getFieldValue('password') === value
                    ? Promise.resolve()
                    : Promise.reject(new Error('两次输入的密码不一致')),
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item style={{ marginBottom: 8 }}>
            <Button type="primary" htmlType="submit" loading={submitting} block>
              注册
            </Button>
          </Form.Item>
          <div style={{ textAlign: 'center', color: '#999' }}>
            已有账号？
            <Link to={redirect !== '/' ? `/login?redirect=${encodeURIComponent(redirect)}` : '/login'}>
              去登录
            </Link>
          </div>
        </Form>
      </Card>
    </AuthPage>
  );
}
