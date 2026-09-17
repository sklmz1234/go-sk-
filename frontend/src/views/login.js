import { login } from '../api.js';
import { saveSession, consumeReturnPath } from '../store.js';
import { errorMessage } from '../utils.js';

export function render(container) {
  container.innerHTML = `
    <main class="auth-page">
      <section class="auth-panel" aria-labelledby="login-title">
        <a class="auth-brand" href="#/products"><span class="brand-mark" aria-hidden="true">M</span>商户管理台</a>
        <div class="auth-heading">
          <p class="eyebrow">商品运营工作区</p>
          ${new URLSearchParams(window.location.hash.split('?')[1] || '').get('registered') === '1' ? '<p class="form-success" role="status">账户已创建，请使用新账户登录。</p>' : ''}
          <h1 id="login-title">欢迎回来</h1>
          <p>登录后可创建、编辑和删除商品。</p>
        </div>
        <form class="auth-form" data-login-form novalidate>
          <div class="field">
            <label for="username">用户名</label>
            <input id="username" name="username" autocomplete="off" required autofocus />
          </div>
          <div class="field">
            <label for="password">密码</label>
            <input id="password" name="password" type="password" autocomplete="new-password" minlength="6" required />
          </div>
          <p class="form-error" data-form-error role="alert" hidden></p>
          <button class="button button--primary button--block" type="submit">登录管理台</button>
        </form>
        <p class="auth-footer">还没有账户？<a href="#/register">创建账户</a></p>
      </section>
      <aside class="auth-aside" aria-hidden="true">
        <div class="auth-aside__content">
          <p>PRODUCT DESK</p>
          <strong>用清晰的数据，
管理每一件商品。</strong>
        </div>
      </aside>
    </main>`;

  const form = container.querySelector('[data-login-form]');
  const error = form.querySelector('[data-form-error]');
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    error.hidden = true;
    const username = form.elements.username.value.trim();
    const password = form.elements.password.value;

    if (!username || password.length < 6) {
      error.textContent = '请输入用户名和至少 6 位密码。';
      error.hidden = false;
      return;
    }

    const submit = form.querySelector('button[type="submit"]');
    submit.disabled = true;
    try {
      const session = await login({ username, password });
      saveSession(session);
      // 优先回到被鉴权守卫拦截前的写操作页面，避免用户重复导航。
      window.location.hash = consumeReturnPath() || '#/products';
    } catch (requestError) {
      error.textContent = errorMessage(requestError);
      error.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
}
