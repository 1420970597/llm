import React from 'react'
import ReactDOM from 'react-dom/client'
import { App as AntApp, ConfigProvider } from 'antd'
import App from './App'
import './styles.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider
      theme={{
        token: {
          colorPrimary: '#2563eb',
          borderRadius: 18,
          colorBgBase: '#f8fbff',
          colorTextBase: '#0f172a',
          colorBorder: 'rgba(148, 163, 184, 0.22)',
          colorFillAlter: 'rgba(248, 250, 252, 0.92)',
          fontFamily: "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
        },
        components: {
          Card: {
            borderRadiusLG: 24,
          },
          Button: {
            borderRadius: 16,
            controlHeight: 42,
          },
          Table: {
            borderColor: 'rgba(148, 163, 184, 0.18)',
            headerBg: 'rgba(248, 250, 252, 0.92)',
            rowHoverBg: 'rgba(239, 246, 255, 0.82)',
          },
        },
      }}
    >
      <AntApp>
        <App />
      </AntApp>
    </ConfigProvider>
  </React.StrictMode>,
)
