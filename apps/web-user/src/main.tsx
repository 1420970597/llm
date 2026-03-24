import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { LocaleProvider } from '@douyinfe/semi-ui'
import zhCN from '@douyinfe/semi-ui/lib/es/locale/source/zh_CN'
import '@douyinfe/semi-ui/dist/css/semi.css'
import App from './App'
import './i18n'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <LocaleProvider locale={zhCN}>
        <App />
      </LocaleProvider>
    </BrowserRouter>
  </React.StrictMode>,
)
