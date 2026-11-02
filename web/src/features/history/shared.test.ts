import { describe, expect, it } from 'vitest'
import type { OciRunResultResponse } from '../../api'
import { ociRunChipProps, runStateChipProps } from './shared'

// ociResult 构造 oci run 的 result_json 解码形态。
function ociResult(overrides: Partial<OciRunResultResponse>): OciRunResultResponse {
    return {
        status: 'success',
        total_images: 3,
        success_count: 3,
        skipped_count: 0,
        failure_count: 0,
        ...overrides,
    }
}

// R36：oci run 的历史状态 chip 由 result_json 推导——success>0 且
// failure>0 → 部分成功；全 failure → 失败；全 skip → 跳过；否则按
// RunState（枚举不扩）。
describe('ociRunChipProps 按 result_json 推导（R36）', () => {
    it('部分成功：success>0 且 failure>0', () => {
        const props = ociRunChipProps('failed', ociResult({ success_count: 2, failure_count: 1 }))
        expect(props).toEqual({ label: '部分成功', color: 'warning' })
    })

    it('全部失败：failure>0 且 success=0', () => {
        const props = ociRunChipProps('failed', ociResult({ success_count: 0, failure_count: 3 }))
        expect(props).toEqual({ label: '失败', color: 'error' })
    })

    it('全部跳过：skip>0 且 success=failure=0', () => {
        const props = ociRunChipProps(
            'succeeded',
            ociResult({ success_count: 0, skipped_count: 3 }),
        )
        expect(props).toEqual({ label: '跳过', color: 'warning' })
    })

    it('全部成功回落 RunState：succeeded → 成功', () => {
        const props = ociRunChipProps('succeeded', ociResult({}))
        expect(props).toEqual(runStateChipProps('succeeded'))
    })

    it('running 无结果（计数字段缺省）回落 RunState', () => {
        const props = ociRunChipProps('running', undefined)
        expect(props).toEqual({ label: '运行中', color: 'info' })
    })
})
