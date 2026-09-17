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
import SourcesPage from './pages/SourcesPage'
import TokensPage from './pages/TokensPage'

// App 定义前端路由：/login 独立于应用骨架，其余路由经 RequireAuth
// 守卫；服务端 SPA fallback 已为深链接预留行为。
export default function App() {
    return (
        <AuthProvider>
            <Routes>
                <Route path="/login" element={<LoginPage />} />
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
                    <Route path="tokens" element={<TokensPage />} />
                    <Route path="*" element={<Navigate to="/" replace />} />
                </Route>
            </Routes>
        </AuthProvider>
    )
}
