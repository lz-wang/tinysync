import ContentCopyOutlinedIcon from '@mui/icons-material/ContentCopyOutlined'
import { Box, IconButton, Snackbar, Tooltip, Typography } from '@mui/material'
import { useCallback, useState } from 'react'
import { useParams } from 'react-router-dom'
import { type FileEntry, listPublicShareEntries, sharedFileURL } from '../../api'
import { copyText } from '../../app/clipboard'
import { usePageTitle } from '../../app/usePageTitle'
import ReadOnlyFileManager from '../../features/files/ReadOnlyFileManager'

// SharedBrowsePage 是 /shared/:slug 公开浏览页：复用文件管理器的
// 位置栏 + 表格工作流，只保留公开分享必需的列（名称 / 类型 / 大小 /
// 修改时间）与操作（下载 / 复制链接）；无受管理状态列，也无隐藏
// 文件开关（公开侧始终隐藏点文件）。单文件共享即单行表格。
export default function SharedBrowsePage() {
    const { slug = '' } = useParams()
    const [path, setPath] = useState('/')
    const [copied, setCopied] = useState(false)
    const [copyError, setCopyError] = useState<string | null>(null)
    usePageTitle('共享内容')

    const load = useCallback(
        async (targetPath: string, cursor?: string) => {
            const page = await listPublicShareEntries(slug, targetPath, 100, cursor)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [slug],
    )

    const copyLink = async (entry: FileEntry) => {
        try {
            await copyText(sharedFileURL(slug, entry.path))
            setCopyError(null)
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
        } catch (error) {
            setCopyError(error instanceof Error ? error.message : String(error))
        }
    }

    return (
        <Box sx={{ minHeight: '100vh', bgcolor: 'background.default', px: 2, py: 3 }}>
            <Typography variant="h5" component="h1" gutterBottom>
                共享内容
            </Typography>
            <ReadOnlyFileManager
                resourceLabel="共享"
                resources={[{ id: slug, label: '共享内容' }]}
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
