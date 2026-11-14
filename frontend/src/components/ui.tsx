import { ReactNode } from 'react'
import { Tooltip } from 'antd'
import { QuestionCircleOutlined } from '@ant-design/icons'

// Field 是弹窗表单里的一行：加粗标签在上、控件在下；inline 时标签在左、控件在右。
export function Field(props: { label: ReactNode; tip?: ReactNode; inline?: boolean; extra?: ReactNode; children: ReactNode }) {
  return (
    <div className={'field' + (props.inline ? ' inline' : '')}>
      <div className="label">
        {props.label}
        {props.tip && (
          <Tooltip title={props.tip}>
            <QuestionCircleOutlined style={{ fontWeight: 400 }} />
          </Tooltip>
        )}
        {props.extra}
      </div>
      {props.inline ? <div style={{ flex: 1, textAlign: 'right' }}>{props.children}</div> : props.children}
    </div>
  )
}

// SettingRow 是设置页里的一行：左边名称，右边控件，各占一半。
export function SettingRow(props: { name: ReactNode; tip?: ReactNode; children: ReactNode }) {
  return (
    <div className="setting-row">
      <div className="name">
        {props.name}{' '}
        {props.tip && (
          <Tooltip title={props.tip}>
            <QuestionCircleOutlined style={{ fontWeight: 400 }} />
          </Tooltip>
        )}
      </div>
      <div className="ctl">{props.children}</div>
    </div>
  )
}

export const NotWired = '此功能只做了界面，没有接入实际业务'
