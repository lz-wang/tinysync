import AddAPhotoOutlinedIcon from '@mui/icons-material/AddAPhotoOutlined'
import KeyOutlinedIcon from '@mui/icons-material/KeyOutlined'
import ManageAccountsOutlinedIcon from '@mui/icons-material/ManageAccountsOutlined'
import TokenOutlinedIcon from '@mui/icons-material/TokenOutlined'
import {
    Avatar,
    Box,
    Button,
    Card,
    CardContent,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    Divider,
    Stack,
    Tab,
    Tabs,
    TextField,
    Typography,
} from '@mui/material'
import { type ChangeEvent, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { updateProfile } from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import { useAuth } from '../features/auth/AuthProvider'
import TokensPage from './TokensPage'

function cropAvatar(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader()
        reader.onerror = () => reject(new Error('读取失败'))
        reader.onload = () => {
            const image = new Image()
            image.onerror = () => reject(new Error('图片无效'))
            image.onload = () => {
                const canvas = document.createElement('canvas')
                canvas.width = 256
                canvas.height = 256
                const context = canvas.getContext('2d')
                if (!context) return reject(new Error('浏览器不支持'))
                const edge = Math.min(image.width, image.height)
                context.drawImage(
                    image,
                    (image.width - edge) / 2,
                    (image.height - edge) / 2,
                    edge,
                    edge,
                    0,
                    0,
                    256,
                    256,
                )
                resolve(canvas.toDataURL('image/jpeg', 0.85))
            }
            image.src = String(reader.result)
        }
        reader.readAsDataURL(file)
    })
}

export default function SettingsPage() {
    const auth = useAuth()
    const location = useLocation()
    const navigate = useNavigate()
    const tab = location.pathname === '/settings/tokens' ? 'developer' : 'account'
    usePageTitle(tab === 'developer' ? '开发者 · 设置' : '帐号设置 · 设置')
    const toast = useToast()
    const [saving, setSaving] = useState(false)
    const [passwordOpen, setPasswordOpen] = useState(false)
    const [current, setCurrent] = useState('')
    const [next, setNext] = useState('')
    const [confirm, setConfirm] = useState('')
    const changeAvatar = async (event: ChangeEvent<HTMLInputElement>) => {
        const file = event.target.files?.[0]
        event.target.value = ''
        if (!file?.type.startsWith('image/')) return
        setSaving(true)
        try {
            await updateProfile({ avatar: await cropAvatar(file) })
            await auth.refreshProfile()
        } catch (reason) {
            toast.error(reason instanceof Error ? reason.message : '头像保存失败')
        } finally {
            setSaving(false)
        }
    }
    const changePassword = async () => {
        if (!current || !next || next !== confirm || [...next].length < 12) return
        setSaving(true)
        try {
            await updateProfile({ current_password: current, new_password: next })
            setPasswordOpen(false)
            toast.success('密码已修改，请使用新密码重新登录。')
            await auth.logout()
        } catch {
            toast.error('密码修改失败，请检查当前密码和新密码。')
        } finally {
            setSaving(false)
        }
    }
    return (
        <Stack spacing={3} sx={{ maxWidth: 900 }}>
            <Tabs
                value={tab}
                onChange={(_, value: 'account' | 'developer') =>
                    navigate(value === 'developer' ? '/settings/tokens' : '/settings')
                }
                aria-label="设置分类"
                sx={{ borderBottom: 1, borderColor: 'divider' }}
            >
                <Tab
                    value="account"
                    icon={<ManageAccountsOutlinedIcon />}
                    iconPosition="start"
                    label="帐号设置"
                />
                <Tab
                    value="developer"
                    icon={<TokenOutlinedIcon />}
                    iconPosition="start"
                    label="开发者设置"
                />
            </Tabs>
            {tab === 'developer' ? (
                <TokensPage />
            ) : (
                <>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={2.5}>
                                <Typography variant="h6">个人资料</Typography>
                                <Divider />
                                <Stack direction="row" spacing={2} sx={{ alignItems: 'center' }}>
                                    <Avatar
                                        src={auth.avatar || undefined}
                                        sx={{
                                            width: 76,
                                            height: 76,
                                            fontSize: 30,
                                            bgcolor: 'primary.main',
                                        }}
                                    >
                                        A
                                    </Avatar>
                                    <Box>
                                        <Button
                                            component="label"
                                            variant="outlined"
                                            startIcon={<AddAPhotoOutlinedIcon />}
                                            disabled={saving}
                                        >
                                            更换头像
                                            <input
                                                hidden
                                                type="file"
                                                accept="image/png,image/jpeg,image/webp"
                                                onChange={event => void changeAvatar(event)}
                                            />
                                        </Button>
                                        {auth.avatar && (
                                            <Button
                                                color="error"
                                                disabled={saving}
                                                onClick={() =>
                                                    void updateProfile({ avatar: '' })
                                                        .then(auth.refreshProfile)
                                                        .catch(() => toast.error('头像移除失败'))
                                                }
                                                sx={{ ml: 1 }}
                                            >
                                                移除
                                            </Button>
                                        )}
                                        <Typography
                                            variant="caption"
                                            color="text.secondary"
                                            sx={{ display: 'block', mt: 1 }}
                                        >
                                            支持 JPG、PNG、WebP，上传后自动居中裁剪。
                                        </Typography>
                                    </Box>
                                </Stack>
                            </Stack>
                        </CardContent>
                    </Card>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={2}>
                                <Typography variant="h6">帐号安全</Typography>
                                <Divider />
                                <Box>
                                    <Button
                                        variant="outlined"
                                        startIcon={<KeyOutlinedIcon />}
                                        onClick={() => setPasswordOpen(true)}
                                    >
                                        修改密码
                                    </Button>
                                    <Typography
                                        variant="caption"
                                        color="text.secondary"
                                        sx={{ display: 'block', mt: 1 }}
                                    >
                                        修改成功后，所有设备均需使用新密码重新登录。
                                    </Typography>
                                </Box>
                            </Stack>
                        </CardContent>
                    </Card>
                    <Dialog
                        open={passwordOpen}
                        onClose={() => !saving && setPasswordOpen(false)}
                        fullWidth
                        maxWidth="xs"
                    >
                        <DialogTitle>修改密码</DialogTitle>
                        <DialogContent>
                            <Stack spacing={2} sx={{ pt: 1 }}>
                                <TextField
                                    label="当前密码"
                                    type="password"
                                    autoComplete="current-password"
                                    value={current}
                                    onChange={event => setCurrent(event.target.value)}
                                />
                                <TextField
                                    label="新密码"
                                    type="password"
                                    autoComplete="new-password"
                                    value={next}
                                    error={next !== '' && [...next].length < 12}
                                    helperText={
                                        next !== '' && [...next].length < 12
                                            ? '新密码至少需要 12 个字符'
                                            : '至少 12 个字符'
                                    }
                                    onChange={event => setNext(event.target.value)}
                                />
                                <TextField
                                    label="确认新密码"
                                    type="password"
                                    autoComplete="new-password"
                                    value={confirm}
                                    error={confirm !== '' && confirm !== next}
                                    helperText={
                                        confirm !== '' && confirm !== next ? '两次输入不一致' : ''
                                    }
                                    onChange={event => setConfirm(event.target.value)}
                                />
                            </Stack>
                        </DialogContent>
                        <DialogActions>
                            <Button disabled={saving} onClick={() => setPasswordOpen(false)}>
                                取消
                            </Button>
                            <Button
                                variant="contained"
                                disabled={
                                    saving ||
                                    !current ||
                                    !next ||
                                    next !== confirm ||
                                    [...next].length < 12
                                }
                                onClick={() => void changePassword()}
                            >
                                确认修改
                            </Button>
                        </DialogActions>
                    </Dialog>
                </>
            )}
        </Stack>
    )
}
