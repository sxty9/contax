import { useMemo, useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
  EmptyState,
  IconButton,
  Panel,
  SearchField,
  Stack,
  Text,
  EyeIcon,
  EyeOffIcon,
  PencilIcon,
  PlusIcon,
  TrashIcon,
  UserIcon,
  useLiveQuery,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Contact, ContactsResponse } from './types';
import { ExternalEditor } from './ExternalEditor';

// The contacts dashboard: every visible contact (internal — computed from shared privleg contact
// groups — plus external, owned by the user), a hidden section, and add/edit/hide/delete actions.
// Internal contacts can only be hidden; external contacts are fully editable and deletable.
export function Dashboard({ api, ui }: ServiceContextProps) {
  const q = useLiveQuery<ContactsResponse>(() => api.get<ContactsResponse>('contacts'), 15000);
  const [search, setSearch] = useState('');
  const [editor, setEditor] = useState<{ open: boolean; contact?: Contact }>({ open: false });

  const contacts = q.data?.contacts ?? [];
  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return contacts;
    return contacts.filter((c) =>
      [c.displayName, c.firstName, c.lastName, c.nickname, c.email].some((f) => f.toLowerCase().includes(s)),
    );
  }, [contacts, search]);
  const visible = filtered.filter((c) => !c.hidden);
  const hidden = filtered.filter((c) => c.hidden);

  async function run(fn: () => Promise<unknown>, okMsg: string) {
    try {
      await fn();
      q.refresh();
      ui.toast({ title: okMsg, variant: 'success' });
    } catch (e) {
      ui.toast({ title: 'Aktion fehlgeschlagen', description: (e as Error).message, variant: 'error' });
    }
  }

  function setHidden(c: Contact, hide: boolean) {
    const verb = hide ? 'hide' : 'unhide';
    const path =
      c.kind === 'internal'
        ? `internal/${encodeURIComponent(c.username ?? c.id)}/${verb}`
        : `contacts/${encodeURIComponent(c.id)}/${verb}`;
    return run(() => api.post(path), hide ? 'Ausgeblendet' : 'Eingeblendet');
  }

  async function remove(c: Contact) {
    const ok = await ui.confirm({
      title: `${c.displayName} löschen?`,
      description: 'Der externe Kontakt wird dauerhaft entfernt.',
      danger: true,
      confirmLabel: 'Löschen',
    });
    if (!ok) return;
    return run(() => api.del(`contacts/${encodeURIComponent(c.id)}`), 'Gelöscht');
  }

  return (
    <Stack gap={4}>
      <Stack direction="row" align="center" justify="between" gap={3} wrap>
        <SearchField value={search} onChange={setSearch} placeholder="Kontakte durchsuchen" className="w-72 max-w-full" />
        <Button variant="primary" iconLeft={<PlusIcon className="h-4 w-4" />} onClick={() => setEditor({ open: true })}>
          Externen Kontakt hinzufügen
        </Button>
      </Stack>

      <Panel title="Kontakte" className="overflow-hidden">
        {visible.length === 0 ? (
          <EmptyState
            icon={<UserIcon />}
            title={search ? 'Keine Treffer' : 'Noch keine Kontakte'}
            description={
              q.loading
                ? 'Lädt…'
                : 'Interne Kontakte erscheinen automatisch, sobald ihr eine Kontaktgruppe teilt. Externe kannst du oben hinzufügen.'
            }
          />
        ) : (
          <Stack gap={0}>
            {visible.map((c) => (
              <ContactRow
                key={rowKey(c)}
                c={c}
                onHide={() => setHidden(c, true)}
                onEdit={() => setEditor({ open: true, contact: c })}
                onDelete={() => remove(c)}
              />
            ))}
          </Stack>
        )}
      </Panel>

      {hidden.length > 0 && (
        <Panel title={`Ausgeblendet (${hidden.length})`} className="overflow-hidden">
          <Stack gap={0}>
            {hidden.map((c) => (
              <ContactRow
                key={rowKey(c)}
                c={c}
                hiddenSection
                onShow={() => setHidden(c, false)}
                onEdit={() => setEditor({ open: true, contact: c })}
                onDelete={() => remove(c)}
              />
            ))}
          </Stack>
        </Panel>
      )}

      <ExternalEditor
        open={editor.open}
        contact={editor.contact}
        api={api}
        ui={ui}
        onClose={() => setEditor({ open: false })}
        onSaved={() => {
          setEditor({ open: false });
          q.refresh();
        }}
      />
    </Stack>
  );
}

function rowKey(c: Contact) {
  return `${c.kind}:${c.id}`;
}

function ContactRow({
  c,
  hiddenSection,
  onHide,
  onShow,
  onEdit,
  onDelete,
}: {
  c: Contact;
  hiddenSection?: boolean;
  onHide?: () => void;
  onShow?: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <Stack direction="row" align="center" gap={3} className="border-b border-separator px-4 py-2.5 last:border-b-0">
      <Avatar name={c.displayName} src={c.avatarUrl || undefined} size={36} />
      <Stack gap={0} className="min-w-0 flex-1">
        <Stack direction="row" align="center" gap={2}>
          <Text weight="medium" truncate>
            {c.displayName}
          </Text>
          <Badge variant={c.kind === 'internal' ? 'accent' : 'neutral'}>{c.kind === 'internal' ? 'Intern' : 'Extern'}</Badge>
        </Stack>
        <Text variant="footnote" color="secondary" truncate>
          {c.email}
        </Text>
      </Stack>
      <Stack direction="row" align="center" gap={1}>
        {c.editable && (
          <IconButton label="Bearbeiten" size="sm" onClick={onEdit}>
            <PencilIcon className="h-4 w-4" />
          </IconButton>
        )}
        {hiddenSection ? (
          <IconButton label="Einblenden" size="sm" onClick={onShow}>
            <EyeIcon className="h-4 w-4" />
          </IconButton>
        ) : (
          <IconButton label="Ausblenden" size="sm" onClick={onHide}>
            <EyeOffIcon className="h-4 w-4" />
          </IconButton>
        )}
        {c.editable && (
          <IconButton label="Löschen" size="sm" onClick={onDelete}>
            <TrashIcon className="h-4 w-4" />
          </IconButton>
        )}
      </Stack>
    </Stack>
  );
}
