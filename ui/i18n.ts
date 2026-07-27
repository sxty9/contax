// Messages for contax, authored in English (US) — the canonical source. Registered on import (see
// index.tsx); the holistic SDK owns the i18n machinery (registerMessages + useT), so contax
// consumes that one shared engine instead of hardcoding strings (Reuse before Build). Other locales
// are added downstream by the nightly translation run. Shared verbs (Cancel, Create, Delete, …) and
// the recipient-picker strings already live in @holistic/ui's core bundle, so they are reused via
// their `common.*` / `contact.*` keys rather than duplicated here. The shell shows the localized
// `service.contax` label in the sidebar, falling back to the plugin's displayName.
import { registerMessages, type MessageVars } from '@holistic/ui';

const count = (v: MessageVars) => Number(v.count ?? 0);

registerMessages({
  'en-US': {
    'service.contax': 'Contacts',

    // Generic
    'contax.loading': 'Loading…',
    'contax.actionFailed': 'Action failed',
    'contax.name': 'Name',
    'contax.save': 'Save',
    'contax.deleteTitle': (v) => `Delete ${v.name}?`,
    'contax.deleted': 'Deleted',
    'contax.deleteFailed': 'Delete failed',

    // Contacts list
    'contax.contacts': 'Contacts',
    'contax.searchContacts': 'Search contacts',
    'contax.addExternalContact': 'Add external contact',
    'contax.noContactsTitle': 'No contacts yet',
    'contax.noContactsBody':
      'Internal contacts appear automatically once you share a contact group. Add external ones above.',
    'contax.internal': 'Internal',
    'contax.external': 'External',
    'contax.edit': 'Edit',
    'contax.hide': 'Hide',
    'contax.show': 'Show',
    'contax.hidden': 'Hidden',
    'contax.shown': 'Shown',

    // Hidden-contacts dialog
    'contax.hiddenContacts': 'Hidden contacts',
    'contax.noHiddenTitle': 'No hidden contacts',
    'contax.noHiddenBody': 'Hidden and re-shown internal and external contacts appear here.',

    // External-contact editor
    'contax.editContact': 'Edit contact',
    'contax.emailRequired': 'An email address is required',
    'contax.saveFailed': 'Save failed',
    'contax.nickname': 'Nickname',
    'contax.firstName': 'First name',
    'contax.lastName': 'Last name',
    'contax.email': 'Email',
    'contax.emailPlaceholder': 'name@example.com',
    'contax.deleteContactBody': 'The external contact will be permanently removed.',

    // Groups
    'contax.groups': 'Groups',
    'contax.createGroup': 'Create group',
    'contax.noGroupsTitle': 'No groups yet',
    'contax.noGroupsBody': 'Group contacts together to address them everywhere at once.',
    'contax.memberCount': (v) => `${count(v)} member${count(v) !== 1 ? 's' : ''}`,
    'contax.editMembers': 'Edit members',
    'contax.deleteGroupBody': 'The group is removed. The contacts themselves are kept.',

    // Create / edit group
    'contax.newGroup': 'New group',
    'contax.groupNameRequired': 'A group name is required',
    'contax.groupNamePlaceholder': 'e.g. Family',
    'contax.createFailed': 'Create failed',
    'contax.editGroup': 'Edit group',
    'contax.renameFailed': 'Rename failed',
    'contax.addMember': 'Add member',
    'contax.noMembersTitle': 'No members yet',
    'contax.noMembersBody': 'Add contacts above.',
    'contax.memberNotAdded': (v) => `${v.name} not added`,
    'contax.removeFailed': 'Remove failed',
  },
});
