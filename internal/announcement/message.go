package announcement

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const CampaignID = "standard-25-release-2026-11"

var emojiIDs = [3]string{"5382357040008021292", "5373260879095686059", "5195033767969839232"}

func emoji(id string) string { return `<tg-emoji emoji-id="` + id + `">⭐</tg-emoji>` }

func Messages() [3]string {
	return [3]string{
		emoji(emojiIDs[0]),
		emoji(emojiIDs[1]),
		fmt.Sprintf(`🇬🇧 <b>English</b>

%s MarketRadar has a fresh update!

More opportunities to analyze the markets: Standard members now get <b>25 successful analyses per day</b>.

Check your account and start analyzing with /start.

🇹🇷 <b>Türkçe</b>

%s MarketRadar'ın yeni güncellemesi yayında!

Piyasaları daha fazla analiz etme fırsatı: Standard üyeleri artık <b>günde 25 başarılı analiz</b> hakkına sahip.

Hesabınızı görüntülemek ve analize başlamak için /start yazın.

🇮🇷 <b>فارسی</b>

%s نسخه جدید MarketRadar در دسترس شماست!

فرصت بیشتری برای بررسی بازار دارید؛ سهمیه پلن Standard اکنون به <b>۲۵ تحلیل موفق در روز</b> افزایش یافته است.

برای مشاهده وضعیت حساب و شروع تحلیل، /start را بزنید.`, emoji(emojiIDs[2]), emoji(emojiIDs[2]), emoji(emojiIDs[2])),
	}
}

func MessageHash() string {
	m := Messages()
	h := sha256.Sum256([]byte(strings.Join(m[:], "\x00")))
	return hex.EncodeToString(h[:])
}

func ExpectedEmojiIDs(sequence int) []string {
	switch sequence {
	case 1:
		return []string{emojiIDs[0]}
	case 2:
		return []string{emojiIDs[1]}
	case 3:
		return []string{emojiIDs[2], emojiIDs[2], emojiIDs[2]}
	default:
		return nil
	}
}
