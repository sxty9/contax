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

// A personal contact group as the list/lookup present it — members are fetched separately
// (groups/<id>/members) so the list stays portioned. This is the entity contax owns and that the
// shared ContactPicker and sibling services reference by id.
export interface GroupSummary {
  id: string;
  name: string;
  memberCount: number;
}

export interface GroupsResponse {
  groups: GroupSummary[];
}
