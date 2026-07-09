import React from 'react'
import ReactDOM from 'react-dom/client'
import { PluginApp } from '@spr-networks/plugin-ui'
import Plugin from './Plugin'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <PluginApp>
      <Plugin />
    </PluginApp>
  </React.StrictMode>
)
