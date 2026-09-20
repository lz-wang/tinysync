import { Navigate, Route, Routes } from 'react-router-dom'
import AppShell from './app/AppShell'
import { AuthProvider } from './features/auth/AuthProvider'
import RequireAuth from './features/auth/RequireAuth'
import FilesPage from './pages/FilesPage'
import HistoryPage from './pages/HistoryPage'
import JobsPage from './pages/JobsPage'
import LoginPage from './pages/LoginPage'
import OverviewPage from './pages/OverviewPage'
import RunDetailPage from './pages/RunDetailPage'
import SettingsPage from './pages/SettingsPage'
import SourcesPage from './pages/SourcesPage'
import SharedBrowsePage from './pages/shared/SharedBrowsePage'

// App 定义前端路由：/login 与 /shared/:slug（公开浏览页）独立于
// 应用骨架与登录守卫，其余路由经 RequireAuth；服务端对 /shared/:slug
// 显式返回 SPA（带 noindex），其余深链接走 SPA fallback。
export default function App() {
    return (
        <AuthProvider>
            <Routes>
                <Route path="/login" element={<LoginPage />} />
                <Route path="/shared/:slug" element={<SharedBrowsePage />} />
                <Route
                    element={
                        <RequireAuth>
                            <AppShell />
                        </RequireAuth>
                    }
                >
                    <Route index element={<OverviewPage />} />
                    <Route path="sources" element={<SourcesPage />} />
                    <Route path="jobs" element={<JobsPage />} />
                    <Route path="files" element={<FilesPage />} />
                    <Route path="history" element={<HistoryPage />} />
                    <Route path="history/:runId" element={<RunDetailPage />} />
                    <Route path="settings" element={<SettingsPage />} />
                    <Route path="settings/tokens" element={<SettingsPage />} />
                    <Route path="*" element={<Navigate to="/" replace />} />
                </Route>
            </Routes>
        </AuthProvider>
    )
}
