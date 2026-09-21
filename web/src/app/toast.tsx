import { Alert, Snackbar } from '@mui/material'
import {
    createContext,
    type ReactNode,
    useCallback,
    useContext,
    useMemo,
    useRef,
    useState,
} from 'react'

// ToastSeverity 对齐 MUI Alert 的 severity：等级决定 filled 配色
//（success 绿 / error 红 / warning 橙 / info 蓝）。
export type ToastSeverity = 'success' | 'error' | 'warning' | 'info'

// ToastApi 是应用层唯一的 toast 入口。同一时刻只展示一条：新 toast
// 替换旧 toast，自动消失计时重新计算。
export interface ToastApi {
    success: (message: string) => void
    error: (message: string) => void
    warning: (message: string) => void
    info: (message: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

// useToast 返回全局 toast 入口；必须在 ToastProvider 之内调用。
export function useToast(): ToastApi {
    const context = useContext(ToastContext)
    if (context === null) throw new Error('useToast must be used within ToastProvider')
    return context
}

interface ToastState {
    key: number
    severity: ToastSeverity
    message: string
}

// autoHideDuration 是 toast 自动消失时长；所有 severity 统一，避免多一个
// 随等级变化的变量。默认右下角由主题 MuiSnackbar.defaultProps 单点声明
//（MUI 出厂默认是左下角，见 providers.tsx），调用点不再重复。
const autoHideDuration = 5000

// ToastProvider 挂载全局唯一的 Snackbar，替代此前各页面自建的
// Snackbar 与内联可关闭 Alert；背景与取舍见 docs/adr/0003-unified-web-toast.md。
export function ToastProvider({ children }: { children: ReactNode }) {
    const [toast, setToast] = useState<ToastState | null>(null)
    const keyRef = useRef(0)
    const show = useCallback((severity: ToastSeverity, message: string) => {
        keyRef.current += 1
        setToast({ key: keyRef.current, severity, message })
    }, [])
    const value = useMemo<ToastApi>(
        () => ({
            success: message => show('success', message),
            error: message => show('error', message),
            warning: message => show('warning', message),
            info: message => show('info', message),
        }),
        [show],
    )
    return (
        <ToastContext.Provider value={value}>
            {children}
            {/* key 随每条 toast 递增：替换展示中的 toast 时强制重挂
                Snackbar，自动消失计时从头计算。 */}
            <Snackbar
                key={toast?.key}
                open={toast !== null}
                autoHideDuration={autoHideDuration}
                onClose={() => setToast(null)}
            >
                <Alert severity={toast?.severity} variant="filled" onClose={() => setToast(null)}>
                    {toast?.message}
                </Alert>
            </Snackbar>
        </ToastContext.Provider>
    )
}
