import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import InsertDriveFileOutlinedIcon from '@mui/icons-material/InsertDriveFileOutlined'
import {
    Alert,
    Box,
    Card,
    CardActionArea,
    CardContent,
    CircularProgress,
    Stack,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { listPublicShares, type PublicShareCard } from '../../api'
import { usePageTitle } from '../../app/usePageTitle'
import { formatDateTime } from '../../features/history/shared'

// SharedIndexPage 是 /shared 公开索引页：以卡片列出全部可服务共享
// （名称、分享时间、过期时间；过期 / 禁用共享不出现在此，ADR-0001）。
// 无登录态要求，也不调用受保护 API——不触发 401 会话语义。
export default function SharedIndexPage() {
    usePageTitle('共享')
    const navigate = useNavigate()
    const [cards, setCards] = useState<PublicShareCard[] | null>(null)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false
        listPublicShares()
            .then(list => {
                if (!cancelled) setCards(list)
            })
            .catch(err => {
                if (!cancelled) setError(err instanceof Error ? err.message : String(err))
            })
        return () => {
            cancelled = true
        }
    }, [])

    return (
        <Box sx={{ minHeight: '100vh', bgcolor: 'background.default', px: 2, py: 4 }}>
            <Typography variant="h5" component="h1" gutterBottom>
                公开共享
            </Typography>
            {error !== null && <Alert severity="error">{error}</Alert>}
            {cards === null && error === null && (
                <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                    <CircularProgress />
                </Box>
            )}
            {cards?.length === 0 && <Alert severity="info">当前没有可访问的共享。</Alert>}
            <Box
                sx={{
                    display: 'grid',
                    gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))',
                    gap: 2,
                    mt: 2,
                }}
            >
                {(cards ?? []).map(card => (
                    <Card key={card.slug} variant="outlined">
                        <CardActionArea onClick={() => navigate(`/shared/${card.slug}`)}>
                            <CardContent>
                                <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                                    {card.is_dir ? (
                                        <FolderOutlinedIcon color="primary" />
                                    ) : (
                                        <InsertDriveFileOutlinedIcon color="primary" />
                                    )}
                                    <Typography noWrap title={card.name}>
                                        {card.name}
                                    </Typography>
                                </Stack>
                                <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
                                    分享于 {formatDateTime(card.created_at)}
                                </Typography>
                                <Typography variant="body2" color="text.secondary">
                                    {card.expires_at === ''
                                        ? '永久有效'
                                        : `过期于 ${formatDateTime(card.expires_at)}`}
                                </Typography>
                            </CardContent>
                        </CardActionArea>
                    </Card>
                ))}
            </Box>
        </Box>
    )
}
