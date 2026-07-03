import { UserIcon, type ServicePlugin } from '@holistic/ui';
import { Dashboard } from './Dashboard';

// contax's dashboard plugin. Linked into holistic/frontend/external/contax at install time and
// discovered by the host SPA's build-time registry. `id` MUST equal the link dir name and the
// permissions manifest's "service" field.
const plugin: ServicePlugin = {
  id: 'contax',
  displayName: 'Kontakte',
  icon: UserIcon,
  order: 100,
  // A personal address book: every signed-in user has one. contax declares no fine-grained
  // rights, so without this the default gate (which hides a right-less service from non-admins)
  // would hide the tab. Who a user SEES internally is governed centrally by privleg's groups.
  visible: () => true,
  Component: Dashboard,
};

export default plugin;
