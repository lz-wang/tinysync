// formatTime 把 RFC3339 时间戳转为本地化展示；空值显示占位符。
export function formatTime(value: string): string {
    return value === '' ? '-' : new Date(value).toLocaleString()
}
