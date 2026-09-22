import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import TextField from '@mui/material/TextField'

// SecretField 是带状态 Chip 与 Clear 按钮的 secret 输入：编辑时保持
// 空白即保留现有值；有 chip 说明当前 Source 已配置该 secret。三态
// 语义（不填保留 / Clear 清除 / 输入替换）与后端 credentials 更新
// 契约一致；各协议表单共用。
export default function SecretField({
    label,
    value,
    dirty,
    cleared,
    chip,
    onChange,
    onClear,
    multiline,
}: {
    label: string
    value: string
    dirty: boolean
    cleared: boolean
    chip: React.ReactNode
    onChange: (value: string) => void
    onClear: () => void
    multiline?: boolean
}) {
    return (
        <Box>
            <TextField
                label={label}
                type={multiline ? undefined : 'password'}
                value={value}
                onChange={e => onChange(e.target.value)}
                fullWidth
                multiline={multiline}
                rows={multiline ? 4 : undefined}
                placeholder={!dirty ? '留空以保留当前值' : undefined}
                sx={multiline ? { '& textarea': { fontFamily: 'monospace' } } : undefined}
            />
            <Box
                sx={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    mt: 1,
                }}
            >
                {chip ?? <Box />}
                {dirty && !cleared && (
                    <Button size="small" onClick={onClear}>
                        清除
                    </Button>
                )}
            </Box>
        </Box>
    )
}
