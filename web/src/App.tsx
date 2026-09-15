import { Navigate, Route, Routes } from 'react-router-dom'
import AppShell from './app/AppShell'
import JobsPage from './pages/JobsPage'
import OverviewPage from './pages/OverviewPage'
import SourcesPage from './pages/SourcesPage'

// App 定义前端路由：服务端 SPA fallback 已为深链接预留行为。
export default function App() {
    return (
        <Routes>
            <Route element={<AppShell />}>
                <Route index element={<OverviewPage />} />
                <Route path="sources" element={<SourcesPage />} />
                <Route path="jobs" element={<JobsPage />} />
                <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
        </Routes>
    )
}
