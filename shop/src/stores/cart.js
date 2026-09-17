import { create } from 'zustand';
import { listCart } from '../api';
import { useSessionStore } from './session';

// 购物车 store 只存角标需要的 total_quantity，不缓存列表——列表只在
// /cart 页用，页面自己拉全量；角标是全局的（顶栏），所以归全局 store。
//
// 不用 persist：购物车已后端持久化（5B 的立项理由），角标每次从服务端
// 同步即可，本地再存一份反而引入"两边对不上"的旧问题。
export const useCartStore = create((set) => ({
  totalQuantity: 0,

  // 任何购物车接口的响应都带最新 total_quantity，变更后就地刷新角标，
  // 不为角标单独发 GET。
  syncFromResponse: (resp) => set({ totalQuantity: resp?.total_quantity || 0 }),

  // 登录后 / 页面刷新时对账：本地没有缓存（见上），但"登录瞬间"角标
  // 需要从 0 变成真实值。游客态清零。
  refresh: async () => {
    if (!useSessionStore.getState().token) {
      set({ totalQuantity: 0 });
      return;
    }
    try {
      const resp = await listCart();
      set({ totalQuantity: resp.total_quantity || 0 });
    } catch {
      // 角标是展示性数据，拉取失败静默降级（页面级操作有自己的错误提示）。
    }
  },

  clear: () => set({ totalQuantity: 0 }),
}));
