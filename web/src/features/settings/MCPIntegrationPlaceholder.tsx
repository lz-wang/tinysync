import ExtensionOutlinedIcon from '@mui/icons-material/ExtensionOutlined'
import { Card, CardContent, Chip, Stack, Typography } from '@mui/material'

// MCPIntegrationPlaceholder 是设置页的静态占位项：MCP 能力已从项目中
// 移除，计划在统一任务管理功能稳定后重新设计。占位不读取配置、不发
// 送任何 API 请求，也不提供任何可交互的开关或状态展示。
export default function MCPIntegrationPlaceholder() {
    return (
        <Card>
            <CardContent>
                <Stack spacing={2} sx={{ alignItems: 'flex-start' }}>
                    <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center' }}>
                        <ExtensionOutlinedIcon color="disabled" />
                        <Typography variant="h6">MCP 集成</Typography>
                        <Chip size="small" label="暂未开放" />
                    </Stack>
                    <Typography variant="body2" color="text.secondary">
                        计划在统一任务管理功能稳定后提供。
                    </Typography>
                </Stack>
            </CardContent>
        </Card>
    )
}
