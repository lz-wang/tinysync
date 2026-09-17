import { Box, CircularProgress } from '@mui/material'
import {
    createContext,
    type ReactNode,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useState,
} from 'react'
import {
    login as apiLogin,
    logout as apiLogout,
    fetchSession,
    unauthorizedEventName,
} from '../../api'

// AuthStatus 是会话状态机：loading（启动探测）、authenticated、
// anonymous（未登录 / 会话失效）。
type AuthStatus = 'loading' | 'authenticated' | 'anonymous'

// AuthContextState 是认证上下文：会话状态 + 登录 / 登出动作 +
// 会话过期标记（登录页据此提示）。
interface AuthContextState {
    status: AuthStatus
    expiresAt: string | null
    sessionExpired: boolean
    login: (password: string) => Promise<void>
    logout: () => Promise<void>
    dismissSessionExpired: () => void
}

const AuthContext = createContext<AuthContextState | null>(null)

// AuthProvider 承载 Web Session 生命周期：启动时探测 /auth/session，
// 登录建立 cookie，登出清除；任何 API 返回 401（全局事件）都会把
// 会话置为 anonymous，由 RequireAuth 重定向到 /login。
export function AuthProvider({ children }: { children: ReactNode }) {
    const [status, setStatus] = useState<AuthStatus>('loading')
    const [expiresAt, setExpiresAt] = useState<string | null>(null)
    const [sessionExpired, setSessionExpired] = useState(false)

    useEffect(() => {
        let cancelled = false
        fetchSession()
            .then(session => {
                if (!cancelled) {
                    setStatus('authenticated')
                    setExpiresAt(session.expires_at)
                }
            })
            .catch(() => {
                if (!cancelled) {
                    setStatus('anonymous')
                }
            })
        return () => {
            cancelled = true
        }
    }, [])

    // 全局 401：仅登录态下响应，避免登录页自身的 401 误标为过期。
    useEffect(() => {
        const onUnauthorized = () => {
            setStatus(current => {
                if (current === 'authenticated') {
                    setExpiresAt(null)
                    setSessionExpired(true)
                    return 'anonymous'
                }
                return current
            })
        }
        window.addEventListener(unauthorizedEventName, onUnauthorized)
        return () => {
            window.removeEventListener(unauthorizedEventName, onUnauthorized)
        }
    }, [])

    const login = useCallback(async (password: string) => {
        const session = await apiLogin(password)
        setExpiresAt(session.expires_at)
        setSessionExpired(false)
        setStatus('authenticated')
    }, [])

    const logout = useCallback(async () => {
        await apiLogout()
        setExpiresAt(null)
        setStatus('anonymous')
    }, [])

    const dismissSessionExpired = useCallback(() => {
        setSessionExpired(false)
    }, [])

    const value = useMemo<AuthContextState>(
        () => ({
            status,
            expiresAt,
            sessionExpired,
            login,
            logout,
            dismissSessionExpired,
        }),
        [status, expiresAt, sessionExpired, login, logout, dismissSessionExpired],
    )

    return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// useAuth 取认证上下文；必须在 AuthProvider 内使用。
export function useAuth(): AuthContextState {
    const context = useContext(AuthContext)
    if (context === null) {
        throw new Error('useAuth must be used within AuthProvider')
    }
    return context
}

// AuthGate 是启动探测期的全屏占位。
export function AuthGate() {
    return (
        <Box
            sx={{
                minHeight: '100vh',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
            }}
        >
            <CircularProgress aria-label="正在验证会话" />
        </Box>
    )
}
