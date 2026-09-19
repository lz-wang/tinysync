import { Alert, Box, Button, Card, CardContent, TextField, Typography } from '@mui/material'
import { type FormEvent, useState } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from '../features/auth/AuthProvider'

// LoginPage 是唯一登录入口：只有密码（单一 Local Admin，无注册 /
// 忘记密码入口）；忘记密码由 operator 在服务器执行
// `tinysync auth set-password`。
export default function LoginPage() {
    const auth = useAuth()
    const [password, setPassword] = useState('')
    const [submitting, setSubmitting] = useState(false)
    const [error, setError] = useState<string | null>(null)

    if (auth.status === 'authenticated') {
        return <Navigate to="/" replace />
    }

    const handleSubmit = async (event: FormEvent) => {
        event.preventDefault()
        if (password === '' || submitting) {
            return
        }
        setSubmitting(true)
        setError(null)
        try {
            await auth.login(password)
        } catch (err) {
            // 统一凭据错误语义：后端不区分原因，这里按 401 提示。
            setError(
                err instanceof Error && err.name === 'UnauthorizedError'
                    ? '密码错误，请重试。'
                    : err instanceof Error
                      ? err.message
                      : String(err),
            )
        } finally {
            setSubmitting(false)
        }
    }

    return (
        <Box
            sx={{
                minHeight: '100vh',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                bgcolor: 'background.default',
                px: 2,
            }}
        >
            <Card variant="outlined" sx={{ width: '100%', maxWidth: 360 }}>
                <CardContent>
                    <Typography variant="h5" component="h1" gutterBottom>
                        TinySync
                    </Typography>
                    <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
                        请输入管理员密码登录。
                    </Typography>
                    {auth.sessionExpired && (
                        <Alert
                            severity="warning"
                            sx={{ mb: 2 }}
                            onClose={auth.dismissSessionExpired}
                        >
                            会话已过期，请重新登录。
                        </Alert>
                    )}
                    <Box component="form" onSubmit={handleSubmit} noValidate>
                        <TextField
                            autoFocus
                            fullWidth
                            required
                            type="password"
                            label="密码"
                            autoComplete="current-password"
                            value={password}
                            onChange={event => setPassword(event.target.value)}
                            disabled={submitting}
                            sx={{ mb: 2 }}
                        />
                        {error !== null && (
                            <Alert severity="error" sx={{ mb: 2 }}>
                                {error}
                            </Alert>
                        )}
                        <Button
                            fullWidth
                            variant="contained"
                            type="submit"
                            disabled={password === '' || submitting}
                        >
                            {submitting ? '登录中…' : '登录'}
                        </Button>
                    </Box>
                    <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ display: 'block', mt: 3 }}
                    >
                        忘记密码？在服务器上执行
                        <Box component="code" sx={{ mx: 0.5 }}>
                            tinysync auth set-password
                        </Box>
                        重置（会立即失效全部会话）。
                    </Typography>
                </CardContent>
            </Card>
        </Box>
    )
}
