import { AuthError } from './api.js';
import { renderAppLayout, setActiveNavigation } from './components/layout.js';
import { showError } from './components/feedback.js';
import { isAuthenticated, setReturnPath } from './store.js';
import * as loginView from './views/login.js';
import * as registerView from './views/register.js';
import * as productsView from './views/products.js';
import * as productDetailView from './views/product-detail.js';
import * as productFormView from './views/product-form.js';

const app = document.querySelector('#app');

function readRoute() {
  const hash = window.location.hash.slice(1) || '/products';
  const [path, queryString = ''] = hash.split('?');
  const query = new URLSearchParams(queryString);
  return { path, query };
}

function showNotFound(container) {
  showError(container, '未找到所请求的页面。');
}

export async function renderRoute() {
  const { path, query } = readRoute();

  // 整站登录门槛：未登录访问任何页面（含默认的 /products）都先跳登录页，
  // 只放行 /login 与 /register 本身。保存原路径可使登录后回到原本要访问的页面。
  if (!isAuthenticated() && path !== '/login' && path !== '/register') {
    setReturnPath(window.location.hash || '#/products');
    window.location.hash = '#/login';
    return;
  }

  try {
    if (path === '/login') {
      loginView.render(app);
      return;
    }
    if (path === '/register') {
      registerView.render(app);
      return;
    }

    renderAppLayout(app);
    const page = app.querySelector('#page-content');
    setActiveNavigation(app, 'products');

    if (path === '/products') {
      const requestedPage = Number(query.get('page'));
      await productsView.render(page, { page: Number.isInteger(requestedPage) && requestedPage > 0 ? requestedPage : 1 });
      return;
    }
    if (path === '/products/new') {
      await productFormView.render(page, { mode: 'create' });
      return;
    }

    const editMatch = path.match(/^\/products\/([^/]+)\/edit$/);
    if (editMatch) {
      await productFormView.render(page, { mode: 'edit', id: decodeURIComponent(editMatch[1]) });
      return;
    }

    const detailMatch = path.match(/^\/products\/([^/]+)$/);
    if (detailMatch) {
      await productDetailView.render(page, { id: decodeURIComponent(detailMatch[1]) });
      return;
    }
    showNotFound(page);
  } catch (error) {
    if (error instanceof AuthError || error?.isAuthError) {
      setReturnPath(window.location.hash || '#/products');
      window.location.hash = '#/login';
      return;
    }
    showError(app, '页面加载失败，请刷新后重试。');
  }
}

export function startRouter() {
  window.addEventListener('hashchange', renderRoute);
  window.addEventListener('autherror', () => {
    // API 请求可能在视图事件中被处理，路由需独立响应认证失效。
    setReturnPath(window.location.hash || '#/products');
    window.location.hash = '#/login';
  });
  renderRoute();
}
