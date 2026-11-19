import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Popconfirm } from 'antd';
import {
  CheckCircleFilled,
  CloudUploadOutlined,
  DeleteOutlined,
  EditOutlined,
  ExportOutlined,
} from '@ant-design/icons';

import { withBasePath } from '@/api/http-init';
import { SizeFormatter } from '@/utils';
import type { StubCard } from '@/schemas/api/nginx';

const SHOT_WIDTH = 1280;

export function stubOpenUrl(card: StubCard): string {
  const query = card.id ? `id=${card.id}` : `key=${encodeURIComponent(card.key)}`;
  return withBasePath(`/panel/api/nginx/stub-open?${query}`);
}

interface StubGalleryProps {
  cards: StubCard[];
  previewVersion: number;
  onChoose: (card: StubCard) => void;
  onEdit: (card: StubCard) => void;
  onDelete: (card: StubCard) => void;
  onNew: () => void;
}

export default function StubGallery({
  cards,
  previewVersion,
  onChoose,
  onEdit,
  onDelete,
  onNew,
}: StubGalleryProps) {
  const { t } = useTranslation();
  const galleryRef = useRef<HTMLDivElement | null>(null);
  const [scale, setScale] = useState(0.33);

  // CSS cannot divide a width by a pixel count, so the tile width is measured here.
  useEffect(() => {
    const gallery = galleryRef.current;
    if (!gallery) return;
    const measure = () => {
      const shot = gallery.querySelector<HTMLElement>('.stub-shot');
      if (shot && shot.clientWidth > 0) setScale(shot.clientWidth / SHOT_WIDTH);
    };
    const observer = new ResizeObserver(measure);
    observer.observe(gallery);
    measure();
    return () => observer.disconnect();
  }, []);

  return (
    <>
      <Alert type="info" showIcon title={t('pages.nginx.stubsDesc')} style={{ marginBottom: 12 }} />
      <div
        className="stub-gallery"
        ref={galleryRef}
        style={{ '--stub-scale': scale } as CSSProperties}
      >
        {cards.map((card) => (
          <div
            key={card.key || `own-${card.id}`}
            className={card.active ? 'stub-card stub-chosen' : 'stub-card'}
          >
            <div className="stub-shot">
              {/* No scripts, no forms, no origin: it is drawn inside the logged-in session. */}
              {card.html !== undefined ? (
                <iframe
                  title={card.name}
                  srcDoc={card.html}
                  sandbox=""
                  scrolling="no"
                  tabIndex={-1}
                  aria-hidden
                />
              ) : (
                <iframe
                  key={previewVersion}
                  title={card.name}
                  src={withBasePath(`/panel/api/nginx/stub/${card.id}/preview?v=${previewVersion}`)}
                  sandbox=""
                  scrolling="no"
                  tabIndex={-1}
                  aria-hidden
                />
              )}
              <button
                type="button"
                className="stub-pick"
                aria-label={`${card.name} — ${card.active ? t('pages.nginx.stubActive') : t('pages.nginx.stubChoose')}`}
                aria-pressed={card.active}
                onClick={() => {
                  if (!card.active) onChoose(card);
                }}
              >
                {card.active ? (
                  <CheckCircleFilled className="stub-mark" aria-hidden />
                ) : (
                  <span className="stub-hint">{t('pages.nginx.stubChoose')}</span>
                )}
              </button>
              <div className="stub-tools stub-tools-left">
                <Button
                  size="small"
                  icon={<ExportOutlined aria-hidden />}
                  title={t('pages.nginx.stubOpenTab')}
                  aria-label={t('pages.nginx.stubOpenTab')}
                  onClick={() => window.open(stubOpenUrl(card), '_blank', 'noopener')}
                />
              </div>
              <div className="stub-tools">
                <Button
                  size="small"
                  icon={<EditOutlined aria-hidden />}
                  title={t('pages.nginx.stubEdit')}
                  aria-label={t('pages.nginx.stubEdit')}
                  onClick={() => onEdit(card)}
                />
                {/* A shipped page has no delete: its tile would come back empty. Edit it first. */}
                {card.id > 0 && !card.active && !card.key && (
                  <Popconfirm
                    title={t('pages.nginx.stubDeleteAsk')}
                    okText={t('sure')}
                    cancelText={t('cancel')}
                    onConfirm={() => onDelete(card)}
                  >
                    <Button
                      size="small"
                      danger
                      icon={<DeleteOutlined aria-hidden />}
                      title={t('delete')}
                      aria-label={t('delete')}
                    />
                  </Popconfirm>
                )}
              </div>
            </div>
            <div className="stub-bar">
              <span className="stub-title">{card.name}</span>
              <span className="stub-weight">{SizeFormatter.sizeFormat(card.size)}</span>
            </div>
          </div>
        ))}
        <button type="button" className="stub-own" onClick={onNew}>
          <CloudUploadOutlined aria-hidden />
          <span>{t('pages.nginx.stubUploadOwn')}</span>
        </button>
      </div>
    </>
  );
}
