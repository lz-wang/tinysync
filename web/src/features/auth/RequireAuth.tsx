import type { ReactNode } from 'react'
import { Navigate } from 'react-router-dom'
import { AuthGate, useAuth } from './AuthProvider'

// RequireAuth 是受保护路由的守卫：会话探测中显示占位，未认证
// 重定向 /login，已认证渲染子树。
export default function RequireAuth({ children }: { children: ReactNode }) {
    const auth = useAuth()
    if (auth.status === 'loading') {
        return <AuthGate />
    }
    if (auth.status === 'anonymous') {
        return <Navigate to="/login" replace />
    }
    return <>{children}</>
}
