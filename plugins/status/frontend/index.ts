/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { definePlugin } from '@/plugin-api.js';
import { initApi } from './api.js';
import StatusCard from './StatusCard.vue';
import StatusSettings from './StatusSettings.vue';
import AdminStats from './AdminStats.vue';

export default definePlugin({
	name: 'status',

	setup(host) {
		initApi(host.api);

		// Vue コンポーネント形式で登録すると、ホストのアプリ内で描画されるので
		// MkInput などが本体と同じ見た目・挙動で動く。
		host.slot('profile:info', { component: StatusCard });

		// 未ログインでは設定画面自体が出ないが、念のため。
		if (host.me != null) {
			host.slot('settings:profile', { component: StatusSettings });
		}

		// 管理画面。/admin/plugin/status で開く。
		//
		// **画面が出るのはモデレーター以上だが、それは UI の都合でしかない。**
		// バックエンド側は Request.IsModerator() で自分で守っている。
		host.adminPage({ path: '/', component: AdminStats });
	},
});
