import { CssBaseline, createTheme, ThemeProvider, useMediaQuery } from '@mui/material'
import {
    createContext,
    type ReactNode,
    useCallback,
    useContext,
    useEffect,
    useMemo,
    useState,
} from 'react'
import { ToastProvider } from './toast'

export type ThemePreference = 'light' | 'dark' | 'system'
interface ColorMode {
    preference: ThemePreference
    setPreference: (preference: ThemePreference) => void
}
const ColorModeContext = createContext<ColorMode | null>(null)
const storageKey = 'tinysync-theme-mode'
export function useColorMode(): ColorMode {
    const context = useContext(ColorModeContext)
    if (context === null) throw new Error('useColorMode must be used within Providers')
    return context
}
export function Providers({ children }: { children: ReactNode }) {
    const prefersDark = useMediaQuery('(prefers-color-scheme: dark)')
    const [preference, setPreferenceState] = useState<ThemePreference>(() => {
        const saved = window.localStorage.getItem(storageKey)
        return saved === 'light' || saved === 'dark' ? saved : 'system'
    })
    const mode = preference === 'system' ? (prefersDark ? 'dark' : 'light') : preference
    useEffect(() => {
        document.documentElement.style.colorScheme = mode
    }, [mode])
    const setPreference = useCallback((value: ThemePreference) => {
        if (value === 'system') window.localStorage.removeItem(storageKey)
        else window.localStorage.setItem(storageKey, value)
        setPreferenceState(value)
    }, [])
    const value = useMemo(() => ({ preference, setPreference }), [preference, setPreference])
    const theme = useMemo(
        () =>
            createTheme({
                palette: {
                    mode,
                    primary: { main: mode === 'dark' ? '#8ab4f8' : '#2563eb' },
                    background:
                        mode === 'dark'
                            ? { default: '#111827', paper: '#182231' }
                            : { default: '#f6f8fc', paper: '#ffffff' },
                },
                shape: { borderRadius: 10 },
                typography: {
                    fontFamily:
                        'ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
                },
                components: {
                    MuiButton: { defaultProps: { disableElevation: true } },
                    // MUI Snackbar 出厂默认在左下角；产品约定 toast 默认右下角，
                    // 在这里单点声明，个别场景可在调用点覆盖。
                    MuiSnackbar: {
                        defaultProps: { anchorOrigin: { vertical: 'bottom', horizontal: 'right' } },
                    },
                },
            }),
        [mode],
    )
    return (
        <ColorModeContext.Provider value={value}>
            <ThemeProvider theme={theme}>
                <CssBaseline />
                <ToastProvider>{children}</ToastProvider>
            </ThemeProvider>
        </ColorModeContext.Provider>
    )
}
