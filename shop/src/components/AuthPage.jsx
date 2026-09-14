// 登录/注册页共用的装饰容器：对角渐变底色 + 两个柔光斑（纯 CSS，零图片资源），
// 卡片居中并加轻投影，让页面在简洁背景上立起来。样式定义在 index.css 的 .auth-page。
export default function AuthPage({ children }) {
  return (
    <div className="auth-page">
      <div className="auth-page-inner">{children}</div>
    </div>
  );
}
