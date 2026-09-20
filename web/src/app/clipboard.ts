// copyText 把文本写入剪贴板。异步 Clipboard API（navigator.clipboard）
// 只在安全上下文（HTTPS、localhost）暴露：以 HTTP 局域网 IP 访问时
// navigator.clipboard 为 undefined，直接调用 writeText 会抛
// "Cannot read properties of undefined (reading 'writeText')"。此时
// 回退到隐藏 textarea + document.execCommand('copy')——该 API 已标记
// deprecated，但各主流浏览器仍支持且不要求安全上下文，是纯 HTTP 部署
// 下唯一可用的同步写剪贴板途径。回退也失败时抛出用户可读的错误，
// 交由调用方决定如何提示。
export async function copyText(text: string): Promise<void> {
    if (navigator.clipboard !== undefined) {
        await navigator.clipboard.writeText(text)
        return
    }
    const textarea = document.createElement('textarea')
    textarea.value = text
    // readOnly + 视口外定位：避免移动端弹出输入法、以及页面滚动跳动。
    textarea.readOnly = true
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    try {
        textarea.select()
        textarea.setSelectionRange(0, text.length)
        if (!document.execCommand('copy')) {
            throw new Error('复制失败：浏览器拒绝了剪贴板写入')
        }
    } finally {
        textarea.remove()
    }
}
