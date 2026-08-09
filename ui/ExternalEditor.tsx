import { useEffect, useState } from 'react';
import { Button, Field, Input, Modal, Stack, type ServiceContextProps } from '@holisdk/ui';
import type { Contact } from './types';

// The add / edit dialog for an EXTERNAL contact. Internal contacts are never edited here — their
// attributes mirror the other user's holistic profile. The picture is derived from the mail
// provider (Gravatar), never uploaded, so there is no image field.
export function ExternalEditor({
  open,
  contact,
  api,
  ui,
  onClose,
  onSaved,
}: {
  open: boolean;
  contact?: Contact;
  api: ServiceContextProps['api'];
  ui: ServiceContextProps['ui'];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [nickname, setNickname] = useState('');
  const [firstName, setFirstName] = useState('');
  const [lastName, setLastName] = useState('');
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);

  // Seed the fields whenever the dialog opens (fresh for an add, prefilled for an edit).
  useEffect(() => {
    if (!open) return;
    setNickname(contact?.nickname ?? '');
    setFirstName(contact?.firstName ?? '');
    setLastName(contact?.lastName ?? '');
    setEmail(contact?.email ?? '');
  }, [open, contact]);

  const editing = !!contact;

  async function save() {
    if (!email.trim()) {
      ui.toast({ title: 'Eine E-Mail-Adresse ist erforderlich', variant: 'error' });
      return;
    }
    setBusy(true);
    try {
      const body = { nickname, firstName, lastName, email };
      if (editing) await api.put(`contacts/${encodeURIComponent(contact!.id)}`, body);
      else await api.post('contacts', body);
      onSaved();
    } catch (e) {
      ui.toast({ title: 'Speichern fehlgeschlagen', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
      title={editing ? 'Kontakt bearbeiten' : 'Externen Kontakt hinzufügen'}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Abbrechen
          </Button>
          <Button variant="primary" loading={busy} onClick={save}>
            Speichern
          </Button>
        </>
      }
    >
      <Stack gap={3}>
        <Field label="Nickname">
          <Input value={nickname} onChange={(e) => setNickname(e.target.value)} />
        </Field>
        <Stack direction="row" gap={3}>
          <Field label="Vorname" className="flex-1">
            <Input value={firstName} onChange={(e) => setFirstName(e.target.value)} />
          </Field>
          <Field label="Nachname" className="flex-1">
            <Input value={lastName} onChange={(e) => setLastName(e.target.value)} />
          </Field>
        </Stack>
        <Field label="E-Mail">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@provider.de" />
        </Field>
      </Stack>
    </Modal>
  );
}
