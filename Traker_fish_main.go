package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func consultarExistenciasHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Error al parsear datos", http.StatusBadRequest)
		return
	}

	entradaUsuario := r.FormValue("pez") // Ej: "Axolotl" o "Axolotl: Agate Gemstone"
	if entradaUsuario == "" {
		http.Error(w, "Falta el nombre del pez", http.StatusBadRequest)
		return
	}

	// Separamos el pez base de su variante si usa ":"
	partes := strings.Split(entradaUsuario, ":")
	pezBase := strings.TrimSpace(partes[0])
	var varianteBuscada string
	if len(partes) > 1 {
		varianteBuscada = strings.TrimSpace(partes[1])
	}

	// 1. Entramos a la lista principal para buscar el enlace del pez base
	urlBase := "https://reef.xs-pets.com/fish"
	client := &http.Client{}
	req, _ := http.NewRequest("GET", urlBase, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		http.Error(w, "Error al conectar con la web de Xundra", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		http.Error(w, "Error leyendo el HTML", http.StatusInternalServerError)
		return
	}

	var linkDetalle string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		textoEnlace := strings.TrimSpace(s.Text())
		if strings.EqualFold(textoEnlace, pezBase) {
			href, existe := s.Attr("href")
			if existe {
				linkDetalle = href
			}
		}
	})

	if linkDetalle == "" {
		w.Write([]byte("Pez base no encontrado en el índice oficial (#FF4500)"))
		return
	}

	if !strings.HasPrefix(linkDetalle, "http") {
		linkDetalle = "https://reef.xs-pets.com/" + strings.TrimPrefix(linkDetalle, "/")
	}

	// 2. Entramos a la página de detalles específica de ese pez
	reqDetalle, _ := http.NewRequest("GET", linkDetalle, nil)
	reqDetalle.Header.Set("User-Agent", "Mozilla/5.0")
	respDetalle, err := client.Do(reqDetalle)
	if err != nil {
		http.Error(w, "Error al entrar al detalle del pez", http.StatusInternalServerError)
		return
	}
	defer respDetalle.Body.Close()

	docDetalle, err := goquery.NewDocumentFromReader(respDetalle.Body)
	if err != nil {
		http.Error(w, "Error leyendo el detalle", http.StatusInternalServerError)
		return
	}

	var resultadoBuilder strings.Builder
	encontrado := false

	// 3. Buscamos las filas o elementos dentro de la página de detalles
	// Dependiendo de cómo esté estructurada la tabla de variantes en su web:
	docDetalle.Find("tr, li, .variant-class").Each(func(i int, s *goquery.Selection) {
		textoFila := strings.TrimSpace(s.Text())
		
		if varianteBuscada != "" {
			// Si el usuario buscó una variante específica (ej: Agate Gemstone)
			if strings.Contains(strings.ToLower(textoFila), strings.ToLower(varianteBuscada)) {
				resultadoBuilder.WriteString(textoFila + " ")
				encontrado = true
			}
		} else {
			// Si solo puso el pez base, recopilamos las variantes/cantidades principales
			if textoFila != "" {
				resultadoBuilder.WriteString(textoFila + " | ")
				encontrado = true
			}
		}
	})

	mensajeFinal := resultadoBuilder.String()
	if !encontrado {
		mensajeFinal = "Variante no encontrada en este pez."
	}

	// Limitamos la longitud por si el texto es muy largo para el chat de SL
	if len(mensajeFinal) > 250 {
		mensajeFinal = mensajeFinal[:247] + "..."
	}

	w.Write([]byte(mensajeFinal))
}

func main() {
	http.HandleFunc("/api/pez", consultarExistenciasHandler)
	fmt.Println("Servidor Go optimizado activo en puerto 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}