import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import FormControl from '@mui/material/FormControl'
import FormControlLabel from '@mui/material/FormControlLabel'
import InputLabel from '@mui/material/InputLabel'
import MenuItem from '@mui/material/MenuItem'
import Select from '@mui/material/Select'
import Switch from '@mui/material/Switch'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { useState } from 'react'
import {
    type GitHubReleaseConfig,
    type GitHubReleasePolicy,
    type GitHubSHA256Mode,
    inspectSource,
    type SourceResponse,
} from '../../api'
import SecretField from './SecretField'

// GitHubReleaseFieldsProps 暴露配置受控回调与 Token 三态输入；预览
// 所需的编辑态 Source 由父组件传入（null 表示创建态）。
interface GitHubReleaseFieldsProps {
    config: GitHubReleaseConfig
    onChange: (config: GitHubReleaseConfig) => void
    token: string
    tokenDirty: boolean
    tokenCleared: boolean
    tokenChip: React.ReactNode
    onTokenChange: (value: string) => void
    onTokenClear: () => void
    source: SourceResponse | null
}

// formatBytes 把字节数转为易读大小（预览表格展示用）。
function formatBytes(size: number): string {
    if (size < 1024) {
        return `${size} B`
    }
    const units = ['KB', 'MB', 'GB', 'TB']
    let value = size
    let unit = 'B'
    for (const next of units) {
        if (value < 1024) {
            break
        }
        value /= 1024
        unit = next
    }
    return `${value.toFixed(1)} ${unit}`
}

// GitHubReleaseFields 是 GitHub Release Source 的专属表单：仓库与
// Token、版本选择策略（按模式动态显示字段）、SHA-256 校验策略，以及
// 「测试并预览」——预览结果为版本 / 发布时间 / 文件数 / 大小 / 状态
// 表格，不下载任何制品。字段约束与后端 ValidateConfig 对齐。
export default function GitHubReleaseFields({
    config,
    onChange,
    token,
    tokenDirty,
    tokenCleared,
    tokenChip,
    onTokenChange,
    onTokenClear,
    source,
}: GitHubReleaseFieldsProps) {
    const [previewing, setPreviewing] = useState(false)
    const [previewError, setPreviewError] = useState<string | null>(null)
    const [preview, setPreview] = useState<{
        repository: string
        releases: {
            tag: string
            name?: string
            prerelease: boolean
            published_at: string
            assetCount: number | null
            totalSize: number | null
        }[]
    } | null>(null)

    function canPreview(): boolean {
        if (previewing) {
            return false
        }
        if (config.repository.trim() === '') {
            return false
        }
        // tag 模式必须已填 Tag 才有预览意义。
        if (config.release_policy === 'tag' && (config.tag ?? '').trim() === '') {
            return false
        }
        return true
    }

    async function handlePreview() {
        setPreviewing(true)
        setPreviewError(null)
        try {
            // 编辑态且表单未改动时用 source_id 形态（服务端读取已存
            // 凭据）；否则直接提交表单值（Token 留空即匿名预览）。
            const unchanged =
                source !== null &&
                !tokenDirty &&
                JSON.stringify(config) === JSON.stringify(source.config)
            const result = await inspectSource(
                source !== null && unchanged
                    ? { source_id: source.id }
                    : {
                          type: 'github_release',
                          config,
                          credentials: tokenDirty ? { token } : undefined,
                      },
            )
            if (!result.ok) {
                setPreviewError(result.error ?? '预览失败')
                setPreview(null)
                return
            }
            setPreview({
                repository: result.repository ?? '',
                releases: (result.releases ?? []).map(rel => ({
                    tag: rel.tag,
                    name: rel.name,
                    prerelease: rel.prerelease,
                    published_at: rel.published_at,
                    assetCount: rel.assets === undefined ? null : rel.assets.length,
                    totalSize:
                        rel.assets === undefined
                            ? null
                            : rel.assets.reduce((sum, a) => sum + a.size, 0),
                })),
            })
        } catch (e) {
            setPreview(null)
            setPreviewError(e instanceof Error ? e.message : String(e))
        } finally {
            setPreviewing(false)
        }
    }

    const policy = config.release_policy

    return (
        <>
            <TextField
                label="GitHub 仓库"
                value={config.repository}
                onChange={e => onChange({ ...config, repository: e.target.value })}
                required
                placeholder="owner/repo 或 https://github.com/owner/repo"
                helperText="接受 owner/repo 或完整的 GitHub 仓库 URL。"
                sx={{ '& input': { fontFamily: 'monospace' } }}
            />
            <SecretField
                label="Personal Access Token（可选）"
                value={token}
                dirty={tokenDirty}
                cleared={tokenCleared}
                chip={tokenChip}
                onChange={onTokenChange}
                onClear={onTokenClear}
            />
            <Typography variant="caption" color="text.secondary">
                公开仓库可匿名访问；私有仓库需要具有相应读取权限的 Token。
            </Typography>
            <FormControl fullWidth>
                <InputLabel id="gh-release-policy-label">版本选择策略</InputLabel>
                <Select
                    labelId="gh-release-policy-label"
                    value={policy}
                    label="版本选择策略"
                    onChange={e =>
                        onChange({
                            ...config,
                            release_policy: e.target.value as GitHubReleasePolicy,
                        })
                    }
                >
                    <MenuItem value="latest">最新稳定版</MenuItem>
                    <MenuItem value="tag">指定 Tag</MenuItem>
                    <MenuItem value="recent">最近 N 个版本</MenuItem>
                    <MenuItem value="all">全部版本</MenuItem>
                </Select>
            </FormControl>
            {policy === 'tag' && (
                <TextField
                    label="指定 Tag"
                    value={config.tag ?? ''}
                    onChange={e => onChange({ ...config, tag: e.target.value })}
                    required
                    placeholder="v1.2.0"
                    sx={{ '& input': { fontFamily: 'monospace' } }}
                />
            )}
            {policy === 'recent' && (
                <TextField
                    label="保留版本数量"
                    type="number"
                    value={config.recent_count ?? 3}
                    onChange={e => onChange({ ...config, recent_count: Number(e.target.value) })}
                    required
                    slotProps={{ htmlInput: { min: 1, max: 1000 } }}
                    helperText="按发布时间选取最近的 N 个已发布 Release（1–1000）。"
                />
            )}
            {policy !== 'latest' && (
                <FormControlLabel
                    control={
                        <Switch
                            checked={config.include_prereleases ?? false}
                            onChange={e =>
                                onChange({ ...config, include_prereleases: e.target.checked })
                            }
                        />
                    }
                    label="包含预发布版本"
                />
            )}
            <FormControl fullWidth>
                <InputLabel id="gh-sha256-label">SHA-256 校验</InputLabel>
                <Select
                    labelId="gh-sha256-label"
                    value={config.verify_sha256}
                    label="SHA-256 校验"
                    onChange={e =>
                        onChange({ ...config, verify_sha256: e.target.value as GitHubSHA256Mode })
                    }
                >
                    <MenuItem value="if_available">GitHub 提供时进行校验</MenuItem>
                    <MenuItem value="required">必须提供（缺失即失败）</MenuItem>
                </Select>
            </FormControl>
            <Box>
                <Button
                    variant="outlined"
                    onClick={() => void handlePreview()}
                    disabled={!canPreview()}
                    startIcon={previewing ? <CircularProgress size={16} /> : undefined}
                >
                    {previewing ? '预览中…' : '测试并预览'}
                </Button>
            </Box>
            {previewError !== null && <Alert severity="error">{previewError}</Alert>}
            {preview !== null && (
                <Box>
                    <Typography variant="caption" color="text.secondary">
                        版本发现结果 · {preview.repository}
                    </Typography>
                    <TableContainer>
                        <Table size="small">
                            <TableHead>
                                <TableRow>
                                    <TableCell>版本</TableCell>
                                    <TableCell>发布时间</TableCell>
                                    <TableCell align="right">文件数</TableCell>
                                    <TableCell align="right">大小</TableCell>
                                    <TableCell align="right">状态</TableCell>
                                </TableRow>
                            </TableHead>
                            <TableBody>
                                {preview.releases.map(rel => (
                                    <TableRow key={rel.tag}>
                                        <TableCell>{rel.tag}</TableCell>
                                        <TableCell>
                                            {new Date(rel.published_at).toLocaleString()}
                                        </TableCell>
                                        <TableCell align="right">{rel.assetCount ?? '—'}</TableCell>
                                        <TableCell align="right">
                                            {rel.totalSize === null
                                                ? '—'
                                                : formatBytes(rel.totalSize)}
                                        </TableCell>
                                        <TableCell align="right">
                                            <Chip
                                                size="small"
                                                color={rel.prerelease ? 'warning' : 'success'}
                                                label={rel.prerelease ? 'Pre-release' : 'Stable'}
                                            />
                                        </TableCell>
                                    </TableRow>
                                ))}
                            </TableBody>
                        </Table>
                    </TableContainer>
                    <Typography variant="caption" color="text.secondary">
                        预览仅发现版本与制品信息，不下载任何文件。
                    </Typography>
                </Box>
            )}
        </>
    )
}
