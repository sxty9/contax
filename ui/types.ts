// Shapes returned by the backend under /api/services/contax/.

// A contact as the dashboard and lookup present it. `internal` contacts are a read-only mirror of
// another holistic user (only hideable); `external` contacts are user-owned and fully editable.
export interface Contact {
  kind: 'internal' | 'external';
  id: string; // internal: username; external: opaque contact id
  username?: string; // internal only
  nickname: string;
  firstName: string;
  lastName: string;
  displayName: string;
  email: string;
  avatarUrl?: string;
  hidden: boolean;
  editable: boolean;
}

export interface ContactsResponse {
  contacts: Contact[];
}

// --- personal ("contax-level") contact groups ---

export type GroupRole = 'owner' | 'admin' | 'member';

// One resolved participant of a group as the dashboard presents it. `ref` is the stable handle used
// by the member mutations: a username for internal members, the email for external members. The
// synthesised owner row is included with role 'owner'.
export interface GroupMemberView {
  kind: 'internal' | 'external';
  ref: string;
  displayName: string;
  email: string;
  avatarUrl?: string;
  role: GroupRole;
}

// A group with the CALLER's own role, so the UI decides which actions to offer without a second call.
export interface Group {
  id: string;
  name: string;
  owner: string; // owner username
  role: GroupRole; // the caller's role in this group
  members: GroupMemberView[];
  memberCount: number;
  created: number;
  updated: number;
}

export interface GroupsResponse {
  groups: Group[];
}
