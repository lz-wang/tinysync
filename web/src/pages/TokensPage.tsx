import { Button, Card, CardContent, Chip, Stack, Typography } from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import {
    type APITokenResponse,
    type CreateAPITokenInput,
    createAPIToken,
    listAPITokens,
    revokeAPIToken,
} from '../api'
import { formatTime } from '../app/formatTime'
import { useToast } from '../app/toast'
import CreateTokenDialog from '../features/tokens/CreateTokenDialog'
import RawTokenDialog from '../features/tokens/RawTokenDialog'
import RevokeTokenDialog from '../features/tokens/RevokeTokenDialog'

// tokenStatus 计算 token 展示状态：撤销优先于过期。
function tokenStatus(token: APITokenResponse): {
    label: string
    color: 'success' | 'warning' | 'error'
} {
    if (token.revoked_at !== '') {
        return { label: '已撤销', color: 'error' }
    }
    if (token.expires_at !== '' && new Date(token.expires_at).getTime() <= Date.now()) {
        return { label: '已过期', color: 'warning' }
    }
    return { label: '有效', color: 'success' }
}

// TokensPage 是 API Token 管理页：列表 / 创建（raw 一次性展示）/
// 撤销。scope 与 expiration 创建后不可变，需要变更时 revoke + 新建。
export default function TokensPage() {
    const toast = useToast()
    const [tokens, setTokens] = useState<APITokenResponse[] | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [createOpen, setCreateOpen] = useState(false)
    const [rawToken, setRawToken] = useState<{ name: string; raw: string } | null>(null)
    const [revokeTarget, setRevokeTarget] = useState<APITokenResponse | null>(null)

    const refresh = useCallback(async () => {
        setLoadError(null)
        try {
            setTokens(await listAPITokens())
        } catch (error) {
            setLoadError(error instanceof Error ? error.message : String(error))
            toast.error(error instanceof Error ? error.message : String(error))
        }
    }, [toast])

    useEffect(() => {
        void refresh()
    }, [refresh])

    const handleCreate = async (input: CreateAPITokenInput): Promise<void> => {
        const response = await createAPIToken(input)
        setCreateOpen(false)
        // raw 只出现一次：交给一次性弹窗展示，关闭即清空 state。
        setRawToken({ name: response.api_token.name, raw: response.raw_token })
        await refresh()
    }

    const handleRevoke = async (id: string): Promise<void> => {
        await revokeAPIToken(id)
        setRevokeTarget(null)
        await refresh()
    }

    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={2}>
                    <Stack
                        direction="row"
                        sx={{ alignItems: 'center', justifyContent: 'space-between' }}
                    >
                        <Button variant="contained" onClick={() => setCreateOpen(true)}>
                            创建 API Token
                        </Button>
                    </Stack>
                    {loadError !== null && (
                        // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
                        <Typography variant="body2" color="text.secondary">
                            API Token 加载失败。
                        </Typography>
                    )}
                    {tokens !== null && tokens.length === 0 && (
                        <Typography variant="body2" color="text.secondary">
                            尚无 API Token，点击“创建 API Token”为自动化脚本创建一个。
                        </Typography>
                    )}
                    {tokens?.map(token => (
                        <Stack
                            key={token.id}
                            direction="row"
                            spacing={2}
                            sx={{
                                p: 1.5,
                                border: 1,
                                borderColor: 'divider',
                                borderRadius: 1,
                                flexWrap: 'wrap',
                                alignItems: 'center',
                            }}
                        >
                            <Stack sx={{ minWidth: 160 }}>
                                <Typography variant="body1" sx={{ fontWeight: 600 }}>
                                    {token.name}
                                </Typography>
                                <Typography
                                    variant="caption"
                                    sx={{ fontFamily: 'monospace' }}
                                    color="text.secondary"
                                >
                                    {token.prefix}…
                                </Typography>
                            </Stack>
                            <Stack direction="row" spacing={0.5}>
                                {token.scopes.map(scope => (
                                    <Chip
                                        key={scope}
                                        label={scope}
                                        size="small"
                                        variant="outlined"
                                    />
                                ))}
                            </Stack>
                            <Chip
                                label={tokenStatus(token).label}
                                color={tokenStatus(token).color}
                                size="small"
                            />
                            <Stack sx={{ minWidth: 220 }}>
                                <Typography variant="caption" color="text.secondary">
                                    过期：{formatTime(token.expires_at)} · 最近使用：
                                    {formatTime(token.last_used_at)}
                                </Typography>
                                <Typography variant="caption" color="text.secondary">
                                    创建：{formatTime(token.created_at)}
                                    {token.revoked_at !== '' &&
                                        ` · 撤销：${formatTime(token.revoked_at)}`}
                                </Typography>
                            </Stack>
                            <Stack direction="row" spacing={1} sx={{ ml: 'auto' }}>
                                {token.revoked_at === '' && (
                                    <Button
                                        size="small"
                                        color="error"
                                        onClick={() => setRevokeTarget(token)}
                                    >
                                        撤销
                                    </Button>
                                )}
                            </Stack>
                        </Stack>
                    ))}
                </Stack>
            </CardContent>

            <CreateTokenDialog
                open={createOpen}
                onClose={() => setCreateOpen(false)}
                onCreated={handleCreate}
            />
            <RawTokenDialog token={rawToken} onClose={() => setRawToken(null)} />
            <RevokeTokenDialog
                token={revokeTarget}
                onClose={() => setRevokeTarget(null)}
                onRevoked={handleRevoke}
            />
        </Card>
    )
}
