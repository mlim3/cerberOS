import { CerberOsLogo } from './icons/CerberOsLogo'
import './SidebarLogo.css'

function SidebarLogo() {
  return (
    <div className="sidebar-logo">
      <div className="sidebar-logo-brand" aria-label="CerberOS">
        <CerberOsLogo className="sidebar-logo-image" title={false} />
        <span className="sidebar-logo-mark">CerberOS</span>
      </div>
    </div>
  )
}

export default SidebarLogo
