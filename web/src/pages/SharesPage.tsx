import { usePageTitle } from '../app/usePageTitle'
import SharePanel from '../features/files/SharePanel'

// SharesPage 是独立的共享策略管理页；共享仍从本地文件浏览器创建，
// 此页集中管理已创建共享的访问、启停、过期与删除。
export default function SharesPage() {
    usePageTitle('共享管理')
    return <SharePanel />
}
