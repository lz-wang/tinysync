import ContentCopyOutlinedIcon from '@mui/icons-material/ContentCopyOutlined'
import { Alert, Box, IconButton, Snackbar, Tooltip, Typography } from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
    type FileEntry,
    listPublicShareEntries,
    listPublicShares,
    type PublicShareCard,
    sharedFileURL,
} from '../../api'
import { usePageTitle } from '../../app/usePageTitle'
import ReadOnlyFileManager from '../../features/files/ReadOnlyFileManager'
import { formatDateTime } from '../../features/history/shared'

// SharedBrowsePage 是 /shared/:slug 公开浏览页：复用文件管理器的
// 位置栏 + 表格工作流，只保留公开分享必需的列（名称 / 类型 / 大小 /
// 修改时间）与操作（下载 / 复制链接）；无受管理状态列，也无隐藏
// 文件开关（公开侧始终隐藏点文件）。单文件共享即单行表格。
export default function SharedBrowsePage() {
    const { slug = '' } = useParams()
    const [path, setPath] = useState('/')
    const [card, setCard] = useState<PublicShareCard | null>(null)
    const [metaError, setMetaError] = useState<string | null>(null)
    const [copied, setCopied] = useState(false)
    const [copyError, setCopyError] = useState<string | null>(null)
    usePageTitle(card?.name ?? '共享内容')

    // 页头元信息来自公开卡片：共享不存在 / 过期时给出明确提示。
    useEffect(() => {
        let cancelled = false
        listPublicShares()
            .then(cards => {
                if (cancelled) return
                setCard(cards.find(item => item.slug === slug) ?? null)
            })
            .catch(err => {
                if (!cancelled) setMetaError(err instanceof Error ? err.message : String(err))
            })
        return () => {
            cancelled = true
        }
    }, [slug])

    const load = useCallback(
        async (targetPath: string, cursor?: string) => {
            const page = await listPublicShareEntries(slug, targetPath, 100, cursor)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [slug],
    )

    const copyLink = async (entry: FileEntry) => {
        try {
            await navigator.clipboard.writeText(sharedFileURL(slug, entry.path))
            setCopyError(null)
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
        } catch (error) {
            setCopyError(error instanceof Error ? error.message : String(error))
        }
    }

    if (metaError !== null) return <Alert severity="error">{metaError}</Alert>
    if (card === null) {
        return <Alert severity="warning">共享不存在或已过期。</Alert>
    }

    return (
        <Box sx={{ minHeight: '100vh', bgcolor: 'background.default', px: 2, py: 3 }}>
            <Typography variant="h5" component="h1" gutterBottom>
                {card.name}
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                分享于 {formatDateTime(card.created_at)}
                {card.expires_at !== '' && ` · 过期于 ${formatDateTime(card.expires_at)}`}
            </Typography>
            <ReadOnlyFileManager
                resourceLabel="共享"
                resources={[{ id: slug, label: card.name }]}
                resourceId={slug}
                path={path}
                onResourceChange={() => undefined}
                onPathChange={setPath}
                load={load}
                downloadURL={entryPath => sharedFileURL(slug, entryPath)}
                renderActions={entry =>
                    entry.kind === 'file' ? (
                        <Tooltip title="复制链接">
                            <IconButton
                                aria-label={`复制 ${entry.name} 的链接`}
                                size="small"
                                onClick={() => void copyLink(entry)}
                            >
                                <ContentCopyOutlinedIcon fontSize="small" />
                            </IconButton>
                        </Tooltip>
                    ) : null
                }
            />
            <Snackbar
                open={copied}
                autoHideDuration={1500}
                onClose={() => setCopied(false)}
                message="链接已复制"
            />
            <Snackbar
                open={copyError !== null}
                autoHideDuration={3000}
                onClose={() => setCopyError(null)}
                message={copyError}
            />
        </Box>
    )
}
